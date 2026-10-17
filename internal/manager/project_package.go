package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"io"
)

const projectEmbeddedJSONLimit = int64(8 << 20)

type projectEmbeddedContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r projectEmbeddedContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(data)
}

// A bounded streaming copy checks actual bytes and CRC, rather than trusting
// the central directory sizes. Read to EOF to retain archive/zip's own checks.
func copyProjectEmbeddedEntry(ctx context.Context, dst io.Writer, entry *zip.File, limit int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if entry.UncompressedSize64 > uint64(limit) {
		return 0, fmt.Errorf("ZIP entry exceeds its %d MiB size limit", limit>>20)
	}
	stream, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	checksum := crc32.NewIEEE()
	n, err := io.CopyBuffer(io.MultiWriter(dst, checksum), io.LimitReader(projectEmbeddedContextReader{ctx, stream}, limit+1), make([]byte, 32<<10))
	if err != nil {
		return n, err
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("ZIP entry exceeds its %d MiB size limit", limit>>20)
	}
	if uint64(n) != entry.UncompressedSize64 {
		return n, fmt.Errorf("ZIP entry actual length does not match its declared length")
	}
	if checksum.Sum32() != entry.CRC32 {
		return n, fmt.Errorf("ZIP entry CRC checksum does not match")
	}
	return n, nil
}

// checkProjectEmbeddedMetadata checks the built HAP/HSP's own identity before
// the caller verifies its SDK signature and installs the same package snapshot.
func checkProjectEmbeddedMetadata(ctx context.Context, filename, bundle string, version int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return fmt.Errorf("HAP/HSP is not a valid ZIP package: %w", err)
	}
	defer archive.Close()
	var descriptor *zip.File
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name != "module.json" {
			continue
		}
		if descriptor != nil {
			return fmt.Errorf("HAP/HSP contains duplicate module.json entries")
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("HAP/HSP module.json must be a regular ZIP entry")
		}
		descriptor = entry
	}
	if descriptor == nil {
		return fmt.Errorf("HAP/HSP contains no root module.json")
	}
	var data bytes.Buffer
	if _, err := copyProjectEmbeddedEntry(ctx, &data, descriptor, projectEmbeddedJSONLimit); err != nil {
		return fmt.Errorf("HAP/HSP module.json must be valid and at most 8 MiB: %w", err)
	}
	var metadata struct {
		App struct {
			Bundle string `json:"bundleName"`
			Code   int64  `json:"versionCode"`
		} `json:"app"`
	}
	if err := decodeProjectJSON(data.Bytes(), &metadata); err != nil {
		return fmt.Errorf("HAP/HSP module.json contains invalid application metadata")
	}
	if metadata.App.Bundle != bundle || metadata.App.Code != version {
		return fmt.Errorf("HAP/HSP bundleName/versionCode does not match the exported project; no application was installed")
	}
	return ctx.Err()
}
