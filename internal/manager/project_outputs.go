package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// packageOutputs returns the exact public module signing outputs for the selected
// product, including outputs that do not exist yet. It neither discovers files
// by suffix nor changes the project or SDK.
func (i *projectInstaller) packageOutputs() ([]string, error) {
	if i.directory == "" {
		return nil, fmt.Errorf("package outputs require a project directory")
	}
	root, err := filepath.Abs(i.directory)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("locate project directory: %w", err)
	}
	product := i.metadata.productName
	if !projectPackageIdentifier(product) {
		return nil, fmt.Errorf("invalid package output product %q", product)
	}
	var outputs []string
	seen := map[string]bool{}
	moduleRoots := map[string]string{}
	hasHAP := false
	for _, raw := range projectArray(i.metadata.profile["modules"]) {
		module := projectMap(raw)
		name, src := projectString(module["name"]), projectString(module["srcPath"])
		if !projectPackageIdentifier(name) || src == "" || filepath.IsAbs(src) || strings.Contains(src, `\`) {
			return nil, fmt.Errorf("invalid package output module declaration %q", name)
		}
		moduleRoot, err := projectPackagePath(root, filepath.Join(root, src))
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		if previous, ok := moduleRoots[name]; ok && previous != moduleRoot {
			return nil, fmt.Errorf("duplicate module %q has different source paths", name)
		}
		moduleRoots[name] = moduleRoot
		targets, err := projectPackageTargets(module, product)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		if len(targets) == 0 {
			continue
		}
		descriptorPath, err := projectPackagePath(root, filepath.Join(moduleRoot, "src/main/module.json5"))
		if err != nil {
			return nil, fmt.Errorf("module %s descriptor: %w", name, err)
		}
		var descriptor struct {
			Module struct {
				Type string `json:"type"`
			} `json:"module"`
		}
		if err := projectJSONFile(descriptorPath, &descriptor); err != nil {
			return nil, fmt.Errorf("read module %s descriptor: %w", name, err)
		}
		extension := ""
		switch descriptor.Module.Type {
		case "entry", "feature":
			extension = ".hap"
		case "shared":
			extension = ".hsp"
		case "har":
			continue
		default:
			return nil, fmt.Errorf("module %s has unsupported module.type %q", name, descriptor.Module.Type)
		}
		artifacts, err := projectPackageArtifactNames(root, moduleRoot)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		for _, target := range targets {
			filename := name + "-" + target + "-signed" + extension
			if artifact := artifacts[target]; artifact != "" {
				// SDK ModuleTargetData.getModuleTargetOutputFileName uses
				// artifactName + extension for signed outputs; only unsigned
				// outputs append "-unsigned" to the configured artifactName.
				filename = artifact + extension
			}
			output, err := projectPackagePath(root, filepath.Join(moduleRoot, "build", product, "outputs", target, filename))
			if err != nil {
				return nil, fmt.Errorf("module %s target %s output: %w", name, target, err)
			}
			if !seen[output] {
				seen[output] = true
				outputs = append(outputs, output)
			}
			hasHAP = hasHAP || extension == ".hap"
		}
	}
	if !hasHAP {
		return nil, fmt.Errorf("product %q has no installable HAP output; at least one entry or feature module target is required", product)
	}
	return outputs, nil
}

func projectPackageIdentifier(name string) bool {
	return projectIdentifier.MatchString(name) && !strings.Contains(name, "..")
}

// Missing root target mappings mean default; explicit mappings only select the
// targets whose applyToProducts contains this product.
func projectPackageTargets(module map[string]any, product string) ([]string, error) {
	raw, exists := module["targets"]
	if !exists {
		return []string{"default"}, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid root target mappings")
	}
	var targets []string
	seen := map[string]bool{}
	for _, entry := range entries {
		target := projectMap(entry)
		name := projectString(target["name"])
		if !projectPackageIdentifier(name) {
			return nil, fmt.Errorf("invalid target identifier %q", name)
		}
		rawProducts, exists := target["applyToProducts"]
		if !exists {
			continue
		}
		products, ok := rawProducts.([]any)
		if !ok {
			return nil, fmt.Errorf("target %s has invalid applyToProducts", name)
		}
		for _, rawProduct := range products {
			mapped, ok := rawProduct.(string)
			if !ok || !projectPackageIdentifier(mapped) {
				return nil, fmt.Errorf("target %s has invalid product identifier", name)
			}
			if mapped == product && !seen[name] {
				seen[name] = true
				targets = append(targets, name)
			}
		}
	}
	return targets, nil
}

func projectPackageArtifactNames(root, moduleRoot string) (map[string]string, error) {
	path, err := projectPackagePath(root, filepath.Join(moduleRoot, "build-profile.json5"))
	if err != nil {
		return nil, err
	}
	var profile map[string]any
	if err := projectJSONFile(path, &profile); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read module build-profile.json5: %w", err)
	}
	artifacts := map[string]string{}
	raw, exists := profile["targets"]
	if !exists {
		return artifacts, nil
	}
	targets, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid module targets")
	}
	for _, rawTarget := range targets {
		target := projectMap(rawTarget)
		name := projectString(target["name"])
		if !projectPackageIdentifier(name) {
			return nil, fmt.Errorf("invalid module target identifier %q", name)
		}
		if _, exists := artifacts[name]; exists {
			return nil, fmt.Errorf("duplicate module target %q", name)
		}
		artifacts[name] = ""
		rawOutput, exists := target["output"]
		if !exists {
			continue
		}
		output := projectMap(rawOutput)
		if output == nil {
			return nil, fmt.Errorf("target %s has invalid output configuration", name)
		}
		rawArtifact, exists := output["artifactName"]
		if !exists {
			continue
		}
		artifact, ok := rawArtifact.(string)
		if !ok || (artifact != "" && (artifact == "." || filepath.IsAbs(artifact) || strings.ContainsAny(artifact, `/\`) || strings.Contains(artifact, "..") || strings.ContainsFunc(artifact, unicode.IsControl))) {
			return nil, fmt.Errorf("target %s has invalid artifactName; expected a single filename", name)
		}
		artifacts[name] = artifact
	}
	return artifacts, nil
}

// Resolve each existing component separately so an absent build directory still
// yields an expected path, while existing symlinks cannot leave the project.
func projectPackagePath(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("package path leaves the project: %s", path)
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return filepath.Join(append([]string{current}, parts[index+1:]...)...), nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect package path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			current, err = filepath.EvalSymlinks(current)
			if err != nil {
				return "", fmt.Errorf("resolve package path symlink: %w", err)
			}
			rel, err := filepath.Rel(root, current)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("package path symlink leaves the project: %s", path)
			}
		}
	}
	return current, nil
}
