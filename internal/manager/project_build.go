package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type projectArtifactStamp struct {
	size     int64
	modified time.Time
}

func projectPackageStamps(paths []string) (map[string]projectArtifactStamp, error) {
	result := map[string]projectArtifactStamp{}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("application output is not a regular file: %s", path)
		}
		result[path] = projectArtifactStamp{info.Size(), info.ModTime()}
	}
	return result, nil
}
func (i *projectInstaller) buildAndInstall(ctx context.Context) (result error) {
	fresh, err := loadProjectMetadata(i.directory, i.metadata.productName)
	if err != nil {
		return err
	}
	if fresh.bundle != i.metadata.bundle || fresh.versionCode != i.metadata.versionCode || fresh.versionName != i.metadata.versionName || !reflect.DeepEqual(fresh.product, i.metadata.product) || !reflect.DeepEqual(fresh.permissions, i.metadata.permissions) || !reflect.DeepEqual(fresh.modules, i.metadata.modules) {
		return fmt.Errorf("project application/product metadata changed during preparation; retry the installation with the intended configuration")
	}
	profilePath := filepath.Join(i.directory, "build-profile.json5")
	original, err := readProjectFile(profilePath)
	if err != nil {
		return err
	}
	// Build signed module packages with the SDK's public tasks. The native
	// SDK's optional all-signed APP extension requires a Java unpacker.
	product := i.metadata.product
	build := projectMap(product["buildOption"])
	if build == nil {
		build = map[string]any{}
		product["buildOption"] = build
	}
	pack := projectMap(build["packOptions"])
	if pack == nil {
		pack = map[string]any{}
		build["packOptions"] = pack
	}
	pack["appWithSignedPkg"] = false
	pack["buildAppSkipSignHap"] = false
	pack["fastBuildApp"] = false
	patched, err := json.MarshalIndent(i.metadata.profile, "", "  ")
	if err != nil {
		return err
	}
	patched = append(patched, '\n')
	recovery, err := projectProfileRecoveryPath(i.directory)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(recovery), 0700); err != nil {
		return err
	}
	backup := projectProfileBackup{SchemaVersion: 1, Original: original, OriginalSHA256: projectDigest(string(original)), PatchedSHA256: projectDigest(string(patched))}
	if err = writeJSON(recovery, backup); err != nil {
		return err
	}
	if err = atomicWrite(profilePath, patched, 0600); err != nil {
		return err
	}
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		if err := restoreProjectProfile(i.directory); err != nil {
			return err
		}
		restored = true
		return nil
	}
	defer func() {
		if err := restore(); err != nil {
			if result == nil {
				result = err
			} else {
				result = fmt.Errorf("%w; %v", result, err)
			}
		}
	}()
	outputs, err := i.packageOutputs()
	if err != nil {
		return err
	}
	if len(outputs) > 64 {
		return fmt.Errorf("the selected product exceeds the 64 application module limit")
	}
	before, err := projectPackageStamps(outputs)
	if err != nil {
		return err
	}
	node := filepath.Join(i.tools.clt, "node/bin/node")
	if err = i.installDependencies(ctx); err != nil {
		return err
	}
	buildEnv, err := i.buildEnvironment()
	if err != nil {
		return err
	}
	fmt.Fprintf(i.m.Out, "Building and signing DevEco product %s (debug).\n", i.metadata.productName)
	if _, err = i.commandWithEnv(ctx, 15*time.Minute, "build signed APP", node, buildEnv, filepath.Join(i.tools.clt, "hvigor/bin/hvigorw.js"), "--mode", "project", "-p", "product="+i.metadata.productName, "-p", "buildMode=debug", "assembleApp", "--no-daemon", "--no-incremental", "--stacktrace"); err != nil {
		return err
	}
	if err = restore(); err != nil {
		return err
	}
	after, err := projectPackageStamps(outputs)
	if err != nil {
		return err
	}
	var total int64
	for _, path := range outputs {
		stamp, exists := after[path]
		previous, old := before[path]
		if !exists || stamp.size <= 0 || (old && previous.size == stamp.size && previous.modified.Equal(stamp.modified)) {
			return fmt.Errorf("the build did not produce a fresh signed application package at %s; no application was installed", path)
		}
		if stamp.size > 2<<30 || stamp.size > (8<<30)-total {
			return fmt.Errorf("signed application packages exceed the 2 GiB module or 8 GiB total limit")
		}
		total += stamp.size
	}
	var packages []string
	for index, artifact := range outputs {
		verifier := *i
		verifier.work = filepath.Join(i.work, fmt.Sprintf("package-%d", index+1))
		if err := os.Mkdir(verifier.work, 0700); err != nil {
			return err
		}
		snapshot, err := verifier.snapshotPackage(ctx, artifact)
		if err != nil {
			return err
		}
		if err = checkProjectEmbeddedMetadata(ctx, snapshot, i.metadata.bundle, i.metadata.versionCode); err != nil {
			return err
		}
		outputProfile := filepath.Join(verifier.work, "profile.p7b")
		outputCertificate := filepath.Join(verifier.work, "certificate.cer")
		if _, err = verifier.command(ctx, 30*time.Second, "verify built application signature", i.tools.verifier, "verify-app", "-inFile", snapshot, "-outCertChain", outputCertificate, "-outProfile", outputProfile); err != nil {
			return err
		}
		if err = verifier.verifyMaterial(ctx, map[string]string{"profile": outputProfile, "certpath": outputCertificate}); err != nil {
			return fmt.Errorf("built application signature is unsuitable for the selected application/device: %w", err)
		}
		packages = append(packages, snapshot)
	}
	identity, err := i.command(ctx, 20*time.Second, "recheck selected device identity", i.tools.hdc, "-t", i.device, "shell", "bm", "get", "-u")
	if err != nil {
		return err
	}
	ids := projectUDID.FindAllString(string(identity), -1)
	if len(ids) != 1 || !strings.EqualFold(ids[0], i.udid) {
		return fmt.Errorf("the selected HDC address now refers to a different or unavailable device; no application was installed")
	}
	fmt.Fprintf(i.m.Out, "Installing %d signed application package(s) on %s with HDC (replace, retain application data).\n", len(packages), i.device)
	installArgs := append([]string{"-t", i.device, "install", "-r"}, packages...)
	output, err := i.command(ctx, 3*time.Minute, "install signed application", i.tools.hdc, installArgs...)
	if err != nil {
		return err
	}
	if !projectOutputSucceeded(output) {
		return fmt.Errorf("HDC did not report successful installation: %s", strings.TrimSpace(string(output)))
	}
	output, err = i.command(ctx, 30*time.Second, "verify installed application version", i.tools.hdc, "-t", i.device, "shell", "bm", "dump", "-n", i.metadata.bundle)
	if err != nil {
		return err
	}
	if !projectInstalledVersion(output, i.metadata.bundle, i.metadata.versionCode) {
		return fmt.Errorf("HDC installation finished, but bundle %s versionCode %d could not be confirmed on the selected device; inspect the device before retrying", i.metadata.bundle, i.metadata.versionCode)
	}
	return nil
}
func projectInstalledVersion(output []byte, bundle string, version int64) bool {
	start, end := strings.IndexByte(string(output), '{'), strings.LastIndexByte(string(output), '}')
	if start < 0 || end < start {
		return false
	}
	var value any
	if json.Unmarshal(output[start:end+1], &value) != nil {
		return false
	}
	var matches func(any) bool
	matches = func(value any) bool {
		switch v := value.(type) {
		case map[string]any:
			name := projectString(v["name"])
			if name == "" {
				name = projectString(v["bundleName"])
			}
			if name == bundle {
				if code, ok := v["versionCode"].(float64); ok && int64(code) == version {
					return true
				}
			}
			for _, child := range v {
				if matches(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if matches(child) {
					return true
				}
			}
		}
		return false
	}
	return matches(value)
}
