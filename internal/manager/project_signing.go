package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (i *projectInstaller) verifyMaterial(ctx context.Context, material map[string]string) error {
	output := filepath.Join(i.work, "verified-profile.json")
	if _, err := i.command(ctx, 30*time.Second, "verify debug Profile", i.tools.verifier, "verify-profile", "-inFile", material["profile"], "-outFile", output); err != nil {
		return err
	}
	data, err := readProjectFile(output)
	if err != nil {
		return err
	}
	certificate, err := readProjectFile(material["certpath"])
	if err != nil {
		return err
	}
	required, err := i.requiredACL()
	if err != nil {
		return err
	}
	return checkProjectProfile(data, i.metadata.bundle, i.udid, required, certificate, time.Now())
}
func (i *projectInstaller) prepareAutomaticSigning(ctx context.Context) error {
	base := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "oheco/export-signing")
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(base, ".cli.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another oo operation is using the developer login/signing service; retry after it finishes: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err = i.ensureCLI(ctx); err != nil {
		return err
	}
	if err = i.login(ctx); err != nil {
		return err
	}
	if err = i.selectTeam(ctx); err != nil {
		return err
	}
	return i.generateSigning(ctx)
}

func (i *projectInstaller) generateSigning(ctx context.Context) error {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("set XDG_CONFIG_HOME to the application's writable private configuration directory before automatic signing")
	}
	key := projectDigest(i.identity + "\x00" + i.metadata.bundle + "\x00" + i.team)
	cache := filepath.Join(root, "oheco/export-signing", key)
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(cache, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another installer is preparing this application's signing configuration: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// The CLI keys its ordinary .ohos cache by absolute project path. This stable
	// minimal project survives exported-directory and package-version changes.
	profilePath := filepath.Join(cache, "build-profile.json5")
	var cached map[string]any
	if err = projectJSONFile(profilePath, &cached); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot reuse cached signing configuration: %w", err)
	}
	data, err := json.Marshal(i.metadata.profile)
	if err != nil {
		return err
	}
	var profile map[string]any
	if err = json.Unmarshal(data, &profile); err != nil {
		return err
	}
	app := map[string]any{}
	for key, value := range projectMap(profile["app"]) {
		app[key] = value
	}
	app["signingConfigs"] = projectMap(cached["app"])["signingConfigs"]
	if app["signingConfigs"] == nil {
		app["signingConfigs"] = []any{}
	}
	profile["app"] = app
	for _, entry := range projectArray(app["products"]) {
		product := projectMap(entry)
		delete(product, "signingConfig")
		for _, previous := range projectArray(projectMap(cached["app"])["products"]) {
			old := projectMap(previous)
			if projectString(old["name"]) == projectString(product["name"]) && projectString(old["signingConfig"]) != "" {
				product["signingConfig"] = old["signingConfig"]
			}
		}
	}
	if err = projectWriteDocument(profilePath, profile); err != nil {
		return err
	}
	for _, relative := range []string{"AppScope/app.json5"} {
		data, e := readProjectFile(filepath.Join(i.directory, relative))
		if e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(filepath.Join(cache, relative)), 0755); e != nil {
			return e
		}
		if e = atomicWrite(filepath.Join(cache, relative), data, 0600); e != nil {
			return e
		}
	}
	for _, entry := range projectArray(i.metadata.profile["modules"]) {
		module := projectMap(entry)
		src := projectString(module["srcPath"])
		for _, relative := range []string{filepath.Join(src, "src/main/module.json5"), filepath.Join(src, "build-profile.json5")} {
			data, e := readProjectFile(filepath.Join(i.directory, relative))
			if os.IsNotExist(e) && filepath.Base(relative) == "build-profile.json5" {
				continue
			}
			if e != nil {
				return e
			}
			destination := filepath.Join(cache, relative)
			if e = os.MkdirAll(filepath.Dir(destination), 0755); e != nil {
				return e
			}
			if e = atomicWrite(destination, data, 0600); e != nil {
				return e
			}
		}
	}
	originalDirectory := i.directory
	i.directory = cache
	_, err = i.cli(ctx, 3*time.Minute, "generate debug signing materials (check developer verification, cloud device registration, team quota and ACL approval)", "signature", "generate", "--team-id", i.team, "--product", i.metadata.productName)
	i.directory = originalDirectory
	if err != nil {
		return err
	}
	generated, err := loadProjectMetadata(cache, i.metadata.productName)
	if err != nil {
		return err
	}
	material, err := generated.material(cache)
	if err != nil {
		return err
	}
	if err = i.verifyMaterial(ctx, material); err != nil {
		return err
	}
	selected := projectString(generated.product["signingConfig"])
	var config map[string]any
	for _, entry := range projectArray(projectMap(generated.profile["app"])["signingConfigs"]) {
		candidate := projectMap(entry)
		if projectString(candidate["name"]) == selected {
			config = candidate
			break
		}
	}
	if config == nil {
		return fmt.Errorf("DevEco CLI did not select one signing configuration")
	}
	// CLI currently writes HarmonyOS even for an OpenHarmony project; retain the
	// actual runtime selected by the exported product for the official builder.
	if runtime := projectString(i.metadata.product["runtimeOS"]); runtime != "" {
		config["type"] = runtime
	}
	current, err := loadProjectMetadata(i.directory, i.metadata.productName)
	if err != nil {
		return err
	}
	if current.bundle != i.metadata.bundle {
		return fmt.Errorf("the exported project bundle changed during signing; retry with the intended project")
	}
	app = projectMap(current.profile["app"])
	var configs []any
	for _, entry := range projectArray(app["signingConfigs"]) {
		if projectString(projectMap(entry)["name"]) != selected {
			configs = append(configs, entry)
		}
	}
	app["signingConfigs"] = append(configs, config)
	current.product["signingConfig"] = selected
	if err = projectWriteDocument(filepath.Join(i.directory, "build-profile.json5"), current.profile); err != nil {
		return err
	}
	i.metadata = current
	return nil
}
