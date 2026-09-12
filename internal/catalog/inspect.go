package catalog

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Inspect reads package metadata from an already downloaded immutable artifact.
// It never extracts or executes package contents.
func Inspect(manager, filename, address string) (any, error) {
	return InspectNamed(manager, filename, filepath.Base(filename), address)
}

// InspectNamed inspects a cached file using its public distribution filename.
func InspectNamed(manager, filename, publicName, address string) (any, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, err
	}
	file := File{URL: address, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Filename: publicName}
	if err := file.Validate(); err != nil {
		return nil, err
	}
	switch manager {
	case "npm":
		if _, err := f.Seek(0, 0); err != nil {
			return nil, err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, 8<<30+1))
		var manifest json.RawMessage
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if strings.TrimPrefix(header.Name, "./") != "package/package.json" {
				continue
			}
			if manifest != nil || header.Typeflag != tar.TypeReg || header.Size > 4<<20 {
				return nil, fmt.Errorf("invalid or duplicate npm package manifest")
			}
			manifest, err = io.ReadAll(io.LimitReader(tr, 4<<20+1))
			if err != nil {
				return nil, err
			}
			var doc map[string]any
			if err := json.Unmarshal(manifest, &doc); err != nil || doc == nil {
				return nil, fmt.Errorf("invalid package.json: %v", err)
			}
		}
		if manifest == nil {
			return nil, fmt.Errorf("missing package/package.json")
		}
		return NpmArtifact{File: file, PackageJSON: manifest}, nil
	case "pip":
		zr, err := zip.NewReader(f, n)
		if err != nil {
			return nil, err
		}
		var metadata textproto.MIMEHeader
		for _, entry := range zr.File {
			if !strings.HasSuffix(entry.Name, ".dist-info/METADATA") {
				continue
			}
			if metadata != nil || entry.UncompressedSize64 > 4<<20 {
				return nil, fmt.Errorf("invalid or duplicate wheel METADATA")
			}
			r, err := entry.Open()
			if err != nil {
				return nil, err
			}
			metadata, err = textproto.NewReader(bufio.NewReader(io.LimitReader(r, 4<<20+1))).ReadMIMEHeader()
			r.Close()
			if err != nil {
				return nil, err
			}
		}
		if metadata == nil {
			return nil, fmt.Errorf("wheel has no METADATA")
		}
		parts := strings.Split(file.Filename, "-")
		if len(parts) < 5 || NormalizePythonName(parts[0]) != NormalizePythonName(metadata.Get("Name")) || parts[1] != metadata.Get("Version") {
			return nil, fmt.Errorf("wheel filename and METADATA name/version differ")
		}
		return PipArtifact{File: file, RequiresPython: metadata.Get("Requires-Python"), RequiresDist: metadata.Values("Requires-Dist")}, nil
	default:
		return nil, fmt.Errorf("inspect requires package-manager pip or npm")
	}
}

// VerifyLanguageMetadata ensures descriptor metadata comes from the actual
// archive. Callers verify size/hash separately before accepting a download.
func VerifyLanguageMetadata(manager, filename string, expected any) error {
	var file File
	switch a := expected.(type) {
	case PipArtifact:
		file = a.File
	case NpmArtifact:
		file = a.File
	default:
		return fmt.Errorf("invalid language artifact")
	}
	actual, err := InspectNamed(manager, filename, file.Filename, file.URL)
	if err != nil {
		return err
	}
	// Cache and temporary filenames need not match the public filename.
	switch a := actual.(type) {
	case PipArtifact:
		a.Filename = file.Filename
		actual = a
	case NpmArtifact:
		a.Filename = file.Filename
		actual = a
	}
	x, err := json.Marshal(actual)
	if err != nil {
		return err
	}
	y, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	var left, right any
	if err := json.Unmarshal(x, &left); err != nil {
		return err
	}
	if err := json.Unmarshal(y, &right); err != nil {
		return err
	}
	if !reflect.DeepEqual(left, right) {
		return fmt.Errorf("descriptor metadata differs from archive %s", file.Filename)
	}
	return nil
}
