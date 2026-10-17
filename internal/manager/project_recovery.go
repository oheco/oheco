package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func projectProfileRecoveryPath(root string) (string, error) {
	return projectRecoveryPath(root, "build-profile.json5")
}

func projectDependencyRecoveryPath(root string) (string, error) {
	return projectRecoveryPath(root, "oh-package.json5")
}

// Only these fixed project files can be journaled. Neither journal contents nor
// callers can supply a path that redirects restoration outside the project.
func projectRecoveryPath(root, filename string) (string, error) {
	identity := root
	switch filename {
	case "build-profile.json5": // Retain the original profile journal path.
	case "oh-package.json5":
		identity += "\x00oh-package.json5"
	default:
		return "", fmt.Errorf("unsupported project recovery file %q", filename)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		return "", fmt.Errorf("set XDG_CONFIG_HOME to the application's writable private configuration directory for installation and interruption recovery")
	}
	return filepath.Join(base, "oheco/export-recovery", projectDigest(identity)+".json"), nil
}

type projectProfileBackup struct {
	SchemaVersion  int    `json:"schema_version"`
	Original       []byte `json:"original"`
	OriginalSHA256 string `json:"original_sha256"`
	PatchedSHA256  string `json:"patched_sha256"`
}

// The journal is committed before modifying the project file, so a killed
// installer can recover on its next invocation. Callers hold the project lock.
func saveProjectRecovery(root, filename string, original, patched []byte) error {
	path, err := projectRecoveryPath(root, filename)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); err == nil {
		return fmt.Errorf("unfinished %s recovery journal already exists: %s", filename, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writeJSON(path, projectProfileBackup{SchemaVersion: 1, Original: original, OriginalSHA256: projectDigest(string(original)), PatchedSHA256: projectDigest(string(patched))})
}

// Keep the existing API for export/resume and build cleanup. Try both journals
// even if one file was edited: each changed file belongs to its user, while the
// other file can still be restored independently.
func restoreProjectProfile(root string) error {
	return errors.Join(restoreProjectConfiguration(root, "build-profile.json5"), restoreProjectDependencies(root))
}

func restoreProjectDependencies(root string) error {
	return restoreProjectConfiguration(root, "oh-package.json5")
}

func restoreProjectConfiguration(root, filename string) error {
	path, err := projectRecoveryPath(root, filename)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return fmt.Errorf("invalid %s recovery journal", filename)
	}
	var backup projectProfileBackup
	if err = json.NewDecoder(file).Decode(&backup); err != nil {
		return fmt.Errorf("invalid %s recovery journal: %w", filename, err)
	}
	if backup.SchemaVersion != 1 || backup.OriginalSHA256 != projectDigest(string(backup.Original)) || len(backup.PatchedSHA256) != 64 {
		return fmt.Errorf("invalid %s recovery journal", filename)
	}
	configuration := filepath.Join(root, filename)
	current, err := readProjectFile(configuration)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, backup.Original) {
		if projectDigest(string(current)) != backup.PatchedSHA256 {
			return fmt.Errorf("%s changed outside this installation; its current contents and %s were preserved for manual recovery", filename, path)
		}
		if err = atomicWrite(configuration, backup.Original, 0600); err != nil {
			return err
		}
	}
	if err = os.Remove(path); err != nil {
		return fmt.Errorf("could not remove completed %s recovery journal: %w", filename, err)
	}
	return nil
}
