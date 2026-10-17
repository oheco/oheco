package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// installDependencies runs the selected CLT's public OHPM command. Hvigor and
// its OHOS plugin are already supplied by that CLT, rather than by OHPM.
// The caller holds the project's export/install lock and recovers old journals
// through restoreProjectProfile before preparing the build.
func (i *projectInstaller) installDependencies(ctx context.Context) (result error) {
	manifest := filepath.Join(i.directory, "oh-package.json5")
	original, err := readProjectFile(manifest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		patched, err := projectOHPMDependencies(original)
		if err != nil {
			return fmt.Errorf("invalid root oh-package.json5: %w", err)
		}
		if patched != nil {
			if err = saveProjectRecovery(i.directory, "oh-package.json5", original, patched); err != nil {
				return err
			}
			// Register restoration before the project write, including write errors.
			// It deliberately leaves the in-progress build profile journal alone.
			defer func() {
				result = errors.Join(result, restoreProjectDependencies(i.directory))
			}()
			current, err := readProjectFile(manifest)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, original) {
				return fmt.Errorf("oh-package.json5 changed before dependency installation; its current contents were preserved")
			}
			if err = atomicWrite(manifest, patched, 0600); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(i.m.Out, "Installing the exported project's OHPM dependencies with the selected Command Line Tools.")
	_, err = i.command(ctx, 10*time.Minute, "install project dependencies", filepath.Join(i.tools.clt, "node/bin/node"), filepath.Join(i.tools.clt, "ohpm/bin/pm-cli.js"), "install", "--all")
	return err
}

func projectOHPMDependencies(original []byte) ([]byte, error) {
	// Keep raw JSON5 values so unrelated fields (including large numbers) do
	// not change through float64 conversion or JSON serialization.
	var document map[string]json.RawMessage
	if err := decodeProjectJSON(original, &document); err != nil {
		return nil, err
	}
	raw, exists := document["devDependencies"]
	if !exists {
		return nil, nil
	}
	var dependencies map[string]json.RawMessage
	if err := decodeProjectJSON(raw, &dependencies); err != nil {
		return nil, err
	}
	changed := false
	for _, name := range []string{"@ohos/hvigor", "@ohos/hvigor-ohos-plugin"} {
		if _, exists := dependencies[name]; exists {
			delete(dependencies, name)
			changed = true
		}
	}
	if !changed {
		return nil, nil
	}
	document["devDependencies"] = projectDependencyDocument(dependencies)
	return projectDependencyDocument(document), nil
}

// Values have already been validated by decodeProjectJSON; retain their JSON5
// syntax verbatim instead of passing JSON5 RawMessages to encoding/json.
func projectDependencyDocument(document map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result bytes.Buffer
	result.WriteString("{\n")
	for index, key := range keys {
		if index > 0 {
			result.WriteString(",\n")
		}
		name, _ := json.Marshal(key)
		result.WriteString("  ")
		result.Write(name)
		result.WriteString(": ")
		result.Write(document[key])
	}
	result.WriteString("\n}\n")
	return result.Bytes()
}
