package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/manager"
)

func verifyProject(client *http.Client, project catalog.Project) error {
	if err := project.Archive().Validate(); err != nil {
		return err
	}
	resp, err := client.Get(project.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", project.URL, resp.Status)
	}
	f, err := os.CreateTemp("", "oo-project-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, project.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != project.Size || hex.EncodeToString(h.Sum(nil)) != project.SHA256 {
		return fmt.Errorf("project size/hash mismatch: %s", project.URL)
	}
	return manager.VerifyProjectArchive(context.Background(), f.Name(), project)
}
