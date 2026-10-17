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
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		return "", fmt.Errorf("set XDG_CONFIG_HOME to the application's writable private configuration directory for installation and interruption recovery")
	}
	return filepath.Join(base, "oheco/export-recovery", projectDigest(root)+".json"), nil
}

type projectProfileBackup struct {
	SchemaVersion  int    `json:"schema_version"`
	Original       []byte `json:"original"`
	OriginalSHA256 string `json:"original_sha256"`
	PatchedSHA256  string `json:"patched_sha256"`
}

// The journal is committed before modifying the profile, so a killed installer
// can recover on its next invocation. A changed file always belongs to its user.
func restoreProjectProfile(root string) error {
	path, err := projectProfileRecoveryPath(root)
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
		return fmt.Errorf("invalid build configuration recovery journal")
	}
	var backup projectProfileBackup
	if err = json.NewDecoder(file).Decode(&backup); err != nil {
		return fmt.Errorf("invalid build configuration recovery journal: %w", err)
	}
	if backup.SchemaVersion != 1 || backup.OriginalSHA256 != projectDigest(string(backup.Original)) || len(backup.PatchedSHA256) != 64 {
		return fmt.Errorf("invalid build configuration recovery journal")
	}
	profile := filepath.Join(root, "build-profile.json5")
	current, err := readProjectFile(profile)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, backup.Original) {
		if projectDigest(string(current)) != backup.PatchedSHA256 {
			return fmt.Errorf("build-profile.json5 changed outside this installation; its current contents and %s were preserved for manual recovery", path)
		}
		if err = atomicWrite(profile, backup.Original, 0600); err != nil {
			return err
		}
	}
	if err = os.Remove(path); err != nil {
		return fmt.Errorf("could not remove completed build configuration journal: %w", err)
	}
	return nil
}
