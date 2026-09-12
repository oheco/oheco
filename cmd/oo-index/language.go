package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/oheco/oheco/internal/catalog"
)

func verifyLanguage(client *http.Client, manager string, file catalog.File, artifact any) error {
	fmt.Printf("Verifying %s artifact %s\n", manager, file.Filename)
	resp, err := client.Get(file.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", file.URL, resp.Status)
	}
	f, err := os.CreateTemp("", "oo-index-artifact-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, file.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != file.Size {
		return fmt.Errorf("artifact size mismatch: %s", file.Filename)
	}
	return catalog.VerifyLanguageMetadata(manager, f.Name(), artifact)
}
