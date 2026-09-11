package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

const maxIndexSize = 32 << 20

func HTTPClient() *http.Client {
	// A nil Transport uses http.DefaultTransport, including its environment
	// proxy settings (HTTPS_PROXY/https_proxy and NO_PROXY/no_proxy).
	return &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many HTTP redirects")
		}
		return catalog.ValidateURL(req.URL.String())
	}}
}
func (m *Manager) get(ctx context.Context, address string) (*http.Response, error) {
	if err := catalog.ValidateURL(address); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "oheco/oo")
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", address, resp.Status)
	}
	return resp, nil
}

func (m *Manager) Update(ctx context.Context) error {
	return m.withLock(func() error {
		resp, err := m.get(ctx, m.IndexURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexSize+1))
		if err != nil {
			return err
		}
		if len(data) > maxIndexSize {
			return fmt.Errorf("index exceeds 32 MiB")
		}
		var idx catalog.Index
		if err := catalog.Decode(bytes.NewReader(data), &idx); err != nil {
			return err
		}
		if err := idx.Validate(); err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(m.Root, "index", "index.json"), data, 0644); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "Updated index: %d packages (%s)\n", len(idx.Packages), idx.GeneratedAt)
		return nil
	})
}
func (m *Manager) LoadIndex() (catalog.Index, error) {
	var idx catalog.Index
	err := readJSON(filepath.Join(m.Root, "index", "index.json"), &idx)
	if errors.Is(err, os.ErrNotExist) {
		return idx, fmt.Errorf("no local index; run oo update first")
	}
	if err != nil {
		return idx, err
	}
	return idx, idx.Validate()
}

func verifyFile(filename string, a catalog.Artifact) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Size {
		return fmt.Errorf("archive size mismatch (expected %d bytes)", a.Size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", a.SHA256, got)
	}
	return nil
}

func (m *Manager) download(ctx context.Context, a catalog.Artifact) (string, error) {
	filename := filepath.Join(m.Root, "cache", "downloads", a.SHA256+"."+a.Format)
	if verifyFile(filename, a) == nil {
		return filename, nil
	}
	fmt.Fprintf(m.Out, "Downloading %s (%d bytes)\n", a.URL, a.Size)
	progress := startDownloadProgress(m.Out, a.Size)
	success := false
	defer func() { progress.finish(success) }()
	resp, err := m.get(ctx, a.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(filepath.Dir(filename), ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h, progress), io.LimitReader(resp.Body, a.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != a.Size {
		return "", fmt.Errorf("archive size mismatch: expected %d, got %d", a.Size, n)
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return "", fmt.Errorf("SHA-256 mismatch for %s", a.URL)
	}
	if err := os.Rename(f.Name(), filename); err != nil {
		return "", err
	}
	success = true
	return filename, nil
}
