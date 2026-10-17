package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const projectSDKCacheRelative = "hvigor/hvigor-ohos-plugin/node_modules/@ohos/hvigor-arkts-compose/dist/src/plugins/node-resolve/cache.js"
const projectSDKHome = "/storage/Users/currentUser"
const projectSDKRootFunction = `function getRootDir(e){return(0,hvigor_common_1.isHarmony)()?"/storage/Users/currentUser":e.startsWith("/")?"/":e.split(":\\")[0]+":\\"}`
const projectSDKSourceLimit = 64 << 10

var projectSDKRootPattern = regexp.MustCompile(`function getRootDir\([^)]*\)\s*\{[^{}]*\}`)

// projectSDKNeedsPatch deliberately recognizes one SDK defect, not arbitrary
// getRootDir implementations. A changed affected function needs a new review.
func projectSDKNeedsPatch(source string) (bool, error) {
	functions := projectSDKRootPattern.FindAllString(source, -1)
	for _, function := range functions {
		if strings.Contains(function, projectSDKHome) {
			if len(functions) != 1 || function != projectSDKRootFunction {
				return false, fmt.Errorf("unsupported SDK getRootDir shape with hardcoded HOME; review the SDK rootDir workaround")
			}
			return true, nil
		}
	}
	if len(functions) == 0 && strings.Contains(source, projectSDKHome) {
		return false, fmt.Errorf("cannot identify SDK getRootDir with hardcoded HOME; review the SDK rootDir workaround")
	}
	return false, nil
}

// Read only a bounded regular cache.js source confined to the selected SDK.
func projectSDKReadSource(clt string) (string, string, error) {
	candidate := filepath.Join(clt, filepath.FromSlash(projectSDKCacheRelative))
	target, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("locate SDK rootDir workaround target: %w", err)
	}
	relative, err := filepath.Rel(clt, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("SDK rootDir workaround target escapes selected SDK: %s", target)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", "", fmt.Errorf("inspect SDK rootDir workaround target: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > projectSDKSourceLimit {
		return "", "", fmt.Errorf("SDK rootDir workaround requires a regular source file up to 64 KiB: %s", target)
	}
	file, err := os.OpenFile(target, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", "", fmt.Errorf("read SDK rootDir workaround target: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return "", "", fmt.Errorf("SDK rootDir workaround target changed while opening: %s", target)
	}
	source, readErr := io.ReadAll(io.LimitReader(file, projectSDKSourceLimit+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", "", fmt.Errorf("read SDK rootDir workaround target: %w", readErr)
	}
	if closeErr != nil {
		return "", "", closeErr
	}
	if len(source) > projectSDKSourceLimit {
		return "", "", fmt.Errorf("SDK rootDir workaround target exceeds 64 KiB: %s", target)
	}
	return target, string(source), nil
}

// buildEnvironment is for the Hvigor build only. CLI, OHPM and HDC retain the
// prepared environment; neither SDK files nor global Node/fs behavior is changed.
func (i *projectInstaller) buildEnvironment() ([]string, error) {
	env := append([]string(nil), i.tools.env...)
	target, source, err := projectSDKReadSource(i.tools.clt)
	if err != nil {
		return nil, err
	}
	needed, err := projectSDKNeedsPatch(source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	if !needed {
		return env, nil
	}
	pathJSON, _ := json.Marshal(target)
	functionJSON, _ := json.Marshal(projectSDKRootFunction)
	// Node's public synchronous hook also covers CommonJS require() in Hvigor.
	preload := `"use strict";
if (process.platform === "openharmony") {
  const { registerHooks } = require("node:module");
  if (typeof registerHooks !== "function") {
    throw new Error("oheco SDK rootDir workaround requires Node.js module.registerHooks");
  }
  const { pathToFileURL } = require("node:url");
  const targetURL = pathToFileURL(` + string(pathJSON) + `).href;
  const knownFunction = ` + string(functionJSON) + `;
  registerHooks({
    load(url, context, nextLoad) {
      const loaded = nextLoad(url, context);
      if (url !== targetURL) return loaded;
      if (loaded.source == null) throw new Error("oheco SDK rootDir workaround: missing SDK module source: " + url);
      const source = typeof loaded.source === "string" ? loaded.source : Buffer.from(loaded.source).toString("utf8");
      const functions = source.match(/function getRootDir\([^)]*\)\s*\{[^{}]*\}/g) || [];
      const affected = functions.filter(body => body.includes("/storage/Users/currentUser"));
      if (!affected.length) {
        if (!functions.length && source.includes("/storage/Users/currentUser")) {
          throw new Error("oheco SDK rootDir workaround: cannot identify getRootDir with hardcoded HOME: " + url);
        }
        return loaded;
      }
      if (functions.length !== 1 || affected[0] !== knownFunction) {
        throw new Error("oheco SDK rootDir workaround: unsupported getRootDir shape with hardcoded HOME: " + url);
      }
      return { ...loaded, source: source.replace(knownFunction, "function getRootDir(e){return e}") };
    }
  });
}
`
	work, err := filepath.Abs(i.work)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(work, "sdk-rootdir-*.cjs")
	if err != nil {
		return nil, fmt.Errorf("create SDK rootDir preload: %w", err)
	}
	preloadPath := file.Name()
	_, writeErr := io.WriteString(file, preload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(preloadPath)
		if writeErr != nil {
			return nil, fmt.Errorf("write SDK rootDir preload: %w", writeErr)
		}
		return nil, closeErr
	}
	// NODE_OPTIONS uses double quotes with backslash escaping, not shell quoting.
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(preloadPath) + `"`
	options := ""
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == "NODE_OPTIONS" {
			options = value
		}
	}
	if options != "" {
		options += " "
	}
	return setLanguageEnv(env, "NODE_OPTIONS", options+"--require="+quoted), nil
}
