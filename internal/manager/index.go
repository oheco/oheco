package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

const backgroundCheckInterval = 30 * time.Minute
const backgroundUpdateTimeout = 30 * time.Second

var ErrUpdateBusy = errors.New("another index update is running")

type indexCache struct {
	URL          string    `json:"url"`
	SHA256       string    `json:"sha256"`
	IndexSource  string    `json:"index_source,omitempty"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	AttemptedAt  time.Time `json:"attempted_at"`
	CheckedAt    time.Time `json:"checked_at"`
	LastError    string    `json:"last_error,omitempty"`
}

// TryUpdateLock acquires a separate index lock without blocking package
// operations. A caller may pass it to a child, closing its own copy afterwards.
// The file must never be unlinked: all invocations must lock the same inode.
func (m *Manager) TryUpdateLock() (*os.File, error) {
	if err := m.prepare(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(m.Root, "state", "index-update.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrUpdateBusy
		}
		return nil, err
	}
	return f, nil
}

func (m *Manager) BackgroundUpdate(ctx context.Context, lock *os.File) error {
	// Validate the inherited descriptor rather than trusting an arbitrary fd 3.
	actual, err := lock.Stat()
	if err != nil {
		return err
	}
	expected, err := os.Stat(filepath.Join(m.Root, "state", "index-update.lock"))
	if err != nil {
		return err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(actual, expected) {
		return fmt.Errorf("invalid background update lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundUpdateTimeout)
	defer cancel()
	return m.refreshIndex(ctx, true)
}

func (m *Manager) Update(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, err := m.TryUpdateLock()
		if err == nil {
			defer lock.Close()
			return m.refreshIndex(ctx, false)
		}
		if !errors.Is(err, ErrUpdateBusy) {
			return err
		}
		// An explicit update waits for the one already in progress; it must
		// neither fail spuriously nor start a second concurrent request.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func decodeIndex(data []byte) (catalog.Index, error) {
	var idx catalog.Index
	if err := catalog.Decode(bytes.NewReader(data), &idx); err != nil {
		return idx, err
	}
	return idx, idx.Validate()
}

func indexDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (m *Manager) refreshIndex(ctx context.Context, automatic bool) (result error) {
	filename := filepath.Join(m.Root, "index", "index.json")
	cacheFilename := filepath.Join(m.Root, "index", "http.json")
	data, _ := os.ReadFile(filename)
	current, currentErr := decodeIndex(data)
	digest := ""
	if currentErr == nil {
		digest = indexDigest(data)
	}
	var cache indexCache
	if err := readJSON(cacheFilename, &cache); err != nil {
		cache = indexCache{}
	}
	previousSource := cache.IndexSource
	if cache.SHA256 != digest {
		previousSource = ""
	}
	// Validators belong to both a source and the exact saved bytes. A crash
	// between the two atomic writes, deletion or local edits invalidate them.
	if cache.URL != m.IndexURL || cache.SHA256 != digest {
		cache = indexCache{URL: m.IndexURL, SHA256: digest, IndexSource: previousSource}
	}
	now := time.Now()
	if elapsed := now.Sub(cache.AttemptedAt); automatic && elapsed >= 0 && elapsed < backgroundCheckInterval {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.AttemptedAt = now
	if err := writeJSON(cacheFilename, cache); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			// Failed checks also back off. Keep the previous valid validators,
			// and record the failure without altering the usable local index.
			cache.LastError = result.Error()
			_ = writeJSON(cacheFilename, cache)
		}
	}()
	if err := catalog.ValidateURL(m.IndexURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.IndexURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "oheco/oo")
	req.Header.Set("Cache-Control", "no-cache")
	conditional := false
	if currentErr == nil {
		if cache.ETag != "" {
			req.Header.Set("If-None-Match", cache.ETag)
			conditional = true
		} else if cache.LastModified != "" {
			req.Header.Set("If-Modified-Since", cache.LastModified)
			conditional = true
		}
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	changed := false
	switch resp.StatusCode {
	case http.StatusNotModified:
		if !conditional {
			return fmt.Errorf("index returned 304 without a usable cached index")
		}
		if etag := resp.Header.Get("ETag"); etag != "" {
			cache.ETag = etag
		}
		if modified := resp.Header.Get("Last-Modified"); modified != "" {
			cache.LastModified = modified
		}
		cache.IndexSource = m.IndexURL
	case http.StatusOK:
		remoteData, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexSize+1))
		if err != nil {
			return err
		}
		if len(remoteData) > maxIndexSize {
			return fmt.Errorf("index exceeds 32 MiB")
		}
		remote, err := decodeIndex(remoteData)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		localTime, _ := time.Parse(time.RFC3339, current.GeneratedAt)
		remoteTime, _ := time.Parse(time.RFC3339, remote.GeneratedAt)
		if automatic && currentErr == nil && (previousSource == "" || previousSource == m.IndexURL) && remoteTime.Before(localTime) {
			// A stale CDN response must not roll back the local index. Its
			// validators cannot describe the newer bytes we retain locally.
			cache.ETag, cache.LastModified = "", ""
		} else {
			changed = !bytes.Equal(data, remoteData)
			if changed {
				if err := atomicWrite(filename, remoteData, 0644); err != nil {
					return err
				}
			}
			current = remote
			cache.SHA256 = indexDigest(remoteData)
			cache.IndexSource = m.IndexURL
			cache.ETag = resp.Header.Get("ETag")
			cache.LastModified = resp.Header.Get("Last-Modified")
		}
	default:
		return fmt.Errorf("GET %s: %s", m.IndexURL, resp.Status)
	}
	cache.CheckedAt = time.Now()
	cache.LastError = ""
	if err := writeJSON(cacheFilename, cache); err != nil {
		return err
	}
	if !automatic {
		if changed {
			fmt.Fprintf(m.Out, "Updated index: %d packages (%s)\n", len(current.Packages), current.GeneratedAt)
		} else {
			fmt.Fprintf(m.Out, "Index is up to date: %d packages (%s)\n", len(current.Packages), current.GeneratedAt)
		}
	}
	return nil
}
