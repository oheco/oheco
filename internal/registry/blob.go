package registry

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/oheco/oheco/internal/catalog"
)

type blob struct {
	URL, Filename, SHA256, Integrity, SHA1 string
	Size                                   int64
	PipArtifact                            *catalog.PipArtifact
	NpmArtifact                            *catalog.NpmArtifact
	mu                                     sync.Mutex
}

func sha256Integrity(value string) string {
	b, _ := hex.DecodeString(value)
	return "sha256-" + base64.StdEncoding.EncodeToString(b)
}

func (b *blob) digest() (hash.Hash, string, error) {
	if b.SHA256 != "" {
		if !catalog.ValidHash(b.SHA256) {
			return nil, "", fmt.Errorf("invalid SHA-256")
		}
		return sha256.New(), b.SHA256, nil
	}
	// SRI picks the strongest supported digest; a malformed SRI must not fall
	// back to a weaker shasum. npm normally publishes sha512.
	for _, algorithm := range []string{"sha512", "sha384", "sha256", "sha1"} {
		for _, token := range strings.Fields(b.Integrity) {
			kind, value, ok := strings.Cut(token, "-")
			if !ok || kind != algorithm {
				continue
			}
			var h hash.Hash
			switch kind {
			case "sha1":
				h = sha1.New()
			case "sha512":
				h = sha512.New()
			case "sha384":
				h = sha512.New384()
			case "sha256":
				h = sha256.New()
			}
			data, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(data) != h.Size() {
				return nil, "", fmt.Errorf("invalid integrity digest")
			}
			return h, hex.EncodeToString(data), nil
		}
	}
	if b.Integrity != "" {
		return nil, "", fmt.Errorf("unsupported integrity digest")
	}
	data, err := hex.DecodeString(b.SHA1)
	if err == nil && len(data) == sha1.Size {
		return sha1.New(), strings.ToLower(b.SHA1), nil
	}
	return nil, "", fmt.Errorf("artifact has no supported integrity digest")
}

func (b *blob) validate() error {
	if err := catalog.ValidateURL(b.URL); err != nil {
		return err
	}
	if !catalog.SafePath(b.Filename) || filepath.Base(b.Filename) != b.Filename || strings.ContainsAny(b.Filename, "%?#\r\n") {
		return fmt.Errorf("invalid artifact filename")
	}
	if b.Size < 0 || b.Size > 8<<30 {
		return fmt.Errorf("invalid artifact size")
	}
	_, _, err := b.digest()
	return err
}

func (b *blob) verify(filename string) bool {
	f, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<30 || b.Size > 0 && info.Size() != b.Size {
		return false
	}
	h, want, err := b.digest()
	if err != nil {
		return false
	}
	_, err = io.Copy(h, f)
	return err == nil && hex.EncodeToString(h.Sum(nil)) == want
}

func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, id string) error {
	s.mu.Lock()
	b := s.blobs[id]
	s.mu.Unlock()
	if b == nil {
		http.NotFound(w, r)
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.MkdirAll(s.Cache, 0755); err != nil {
		return err
	}
	key := sha256.Sum256([]byte(b.SHA256 + "\n" + b.Integrity + "\n" + b.SHA1))
	filename := filepath.Join(s.Cache, hex.EncodeToString(key[:])+".blob")
	if !b.verify(filename) {
		req, err := http.NewRequestWithContext(r.Context(), "GET", b.URL, nil)
		if err != nil {
			return err
		}
		resp, err := s.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("artifact download: HTTP %d", resp.StatusCode)
		}
		f, err := os.CreateTemp(s.Cache, ".registry-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		h, want, err := b.digest()
		if err != nil {
			f.Close()
			return err
		}
		limit := int64(8 << 30)
		if b.Size > 0 {
			limit = b.Size
		}
		n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n > limit || b.Size > 0 && n != b.Size || hex.EncodeToString(h.Sum(nil)) != want {
			return fmt.Errorf("artifact size/integrity mismatch for %s", b.Filename)
		}
		if err := os.Rename(f.Name(), filename); err != nil {
			return err
		}
	}
	if b.PipArtifact != nil {
		if err := catalog.VerifyLanguageMetadata("pip", filename, *b.PipArtifact); err != nil {
			return err
		}
	}
	if b.NpmArtifact != nil {
		if err := catalog.VerifyLanguageMetadata("npm", filename, *b.NpmArtifact); err != nil {
			return err
		}
	}
	if strings.HasSuffix(b.Filename, ".whl") {
		a, err := catalog.InspectNamed("pip", filename, b.Filename, b.URL)
		if err != nil {
			return err
		}
		for _, requirement := range a.(catalog.PipArtifact).RequiresDist {
			if strings.Contains(requirement, "@") {
				return fmt.Errorf("wheel contains a direct URL dependency: %s", requirement)
			}
		}
	} else if strings.HasSuffix(b.Filename, ".tgz") {
		a, err := catalog.InspectNamed("npm", filename, b.Filename, b.URL)
		if err != nil {
			return err
		}
		var manifest map[string]any
		if err := json.Unmarshal(a.(catalog.NpmArtifact).PackageJSON, &manifest); err != nil {
			return err
		}
		if err := ValidateNpmDependencies(manifest); err != nil {
			return err
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filename)
	return nil
}
