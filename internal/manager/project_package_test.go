package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"testing"
)

const projectPackageTestMetadata = `{"app":{"bundleName":"org.oheco.exportfixture","versionCode":12}}`

type projectEmbeddedZIPEntry struct {
	name string
	data []byte
	mode os.FileMode
	raw  bool
	size uint64
	crc  uint32
}

func projectEmbeddedZIP(t *testing.T, entries ...projectEmbeddedZIPEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		var file io.Writer
		var err error
		if entry.raw {
			header.UncompressedSize64, header.CompressedSize64, header.CRC32 = entry.size, uint64(len(entry.data)), entry.crc
			file, err = writer.CreateRaw(header)
		} else {
			file, err = writer.CreateHeader(header)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestProjectPackageMetadataHAPAndHSP(t *testing.T) {
	for _, extension := range []string{".hap", ".hsp"} {
		t.Run(extension, func(t *testing.T) {
			data := projectEmbeddedZIP(t,
				projectEmbeddedZIPEntry{name: "module.json", data: []byte(projectPackageTestMetadata)},
				projectEmbeddedZIPEntry{name: "nested/module.json", data: []byte(`{"app":{"bundleName":"org.other.application","versionCode":1}}`)})
			filename := projectTestFile(t, t.TempDir(), "package"+extension, data)
			if err := checkProjectEmbeddedMetadata(context.Background(), filename, "org.oheco.exportfixture", 12); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectPackageMetadataIdentity(t *testing.T) {
	filename := projectTestFile(t, t.TempDir(), "package.hap", projectEmbeddedZIP(t,
		projectEmbeddedZIPEntry{name: "module.json", data: []byte(projectPackageTestMetadata)}))
	for _, tc := range []struct {
		name, bundle string
		version      int64
	}{
		{"other-bundle", "org.other.application", 12},
		{"old-version", "org.oheco.exportfixture", 13},
		{"new-version", "org.oheco.exportfixture", 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkProjectEmbeddedMetadata(context.Background(), filename, tc.bundle, tc.version)
			if err == nil || !strings.Contains(err.Error(), "bundleName/versionCode") {
				t.Fatalf("accepted mismatched application identity: %v", err)
			}
		})
	}
}

func TestProjectEmbeddedMetadataRejectsInvalidEntries(t *testing.T) {
	valid := []byte(projectPackageTestMetadata)
	for _, tc := range []struct {
		name    string
		entries []projectEmbeddedZIPEntry
		want    string
	}{
		{"duplicate", []projectEmbeddedZIPEntry{{name: "module.json", data: valid}, {name: "module.json", data: valid}}, "duplicate module.json"},
		{"symlink", []projectEmbeddedZIPEntry{{name: "module.json", data: []byte("target"), mode: os.ModeSymlink | 0600}}, "regular ZIP entry"},
		{"fifo", []projectEmbeddedZIPEntry{{name: "module.json", mode: os.ModeNamedPipe | 0600}}, "regular ZIP entry"},
		{"directory", []projectEmbeddedZIPEntry{{name: "module.json", mode: os.ModeDir | 0700}}, "regular ZIP entry"},
		{"missing-root", []projectEmbeddedZIPEntry{{name: "nested/module.json", data: valid}}, "no root module.json"},
		{"missing-app", []projectEmbeddedZIPEntry{{name: "module.json", data: []byte(`{"module":{"bundleName":"org.oheco.exportfixture","versionCode":12}}`)}}, "bundleName/versionCode"},
		{"malformed", []projectEmbeddedZIPEntry{{name: "module.json", data: []byte(`{bad`)}}, "invalid application metadata"},
		{"declared-oversized", []projectEmbeddedZIPEntry{{name: "module.json", raw: true, size: (8 << 20) + 1}}, "8 MiB"},
		{"bad-crc", []projectEmbeddedZIPEntry{{name: "module.json", data: valid, raw: true, size: uint64(len(valid)), crc: crc32.ChecksumIEEE(valid) ^ 1}}, "checksum"},
		{"zero-crc", []projectEmbeddedZIPEntry{{name: "module.json", data: valid, raw: true, size: uint64(len(valid))}}, "checksum"},
		{"short-data", []projectEmbeddedZIPEntry{{name: "module.json", data: valid, raw: true, size: uint64(len(valid) + 1), crc: crc32.ChecksumIEEE(valid)}}, ""},
		{"long-data", []projectEmbeddedZIPEntry{{name: "module.json", data: valid, raw: true, size: uint64(len(valid) - 1), crc: crc32.ChecksumIEEE(valid)}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename := projectTestFile(t, t.TempDir(), "package.hsp", projectEmbeddedZIP(t, tc.entries...))
			err := checkProjectEmbeddedMetadata(context.Background(), filename, "org.oheco.exportfixture", 12)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("accepted invalid package metadata: %v", err)
			}
		})
	}
}

func TestProjectEmbeddedMetadataSizeBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		name := "exactly-8MiB"
		if extra != 0 {
			name = "over-8MiB"
		}
		t.Run(name, func(t *testing.T) {
			data := append([]byte(projectPackageTestMetadata), bytes.Repeat([]byte(" "), int(projectEmbeddedJSONLimit)-len(projectPackageTestMetadata)+extra)...)
			filename := projectTestFile(t, t.TempDir(), "package.hap", projectEmbeddedZIP(t,
				projectEmbeddedZIPEntry{name: "module.json", data: data}))
			err := checkProjectEmbeddedMetadata(context.Background(), filename, "org.oheco.exportfixture", 12)
			if extra == 0 && err != nil {
				t.Fatalf("rejected metadata at the size limit: %v", err)
			}
			if extra != 0 && (err == nil || !strings.Contains(err.Error(), "8 MiB")) {
				t.Fatalf("accepted metadata over the size limit: %v", err)
			}
		})
	}
}

func TestProjectEmbeddedMetadataInvalidZIPAndCancellation(t *testing.T) {
	filename := projectTestFile(t, t.TempDir(), "package.hap", []byte("not a ZIP"))
	if err := checkProjectEmbeddedMetadata(context.Background(), filename, "org.oheco.exportfixture", 12); err == nil || !errors.Is(err, zip.ErrFormat) {
		t.Fatalf("accepted invalid ZIP package: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkProjectEmbeddedMetadata(ctx, filename, "org.oheco.exportfixture", 12); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored canceled metadata check: %v", err)
	}
}

func TestProjectPackageMetadataAPKSigningBlock(t *testing.T) {
	data := projectEmbeddedZIP(t, projectEmbeddedZIPEntry{name: "module.json", data: []byte(projectPackageTestMetadata)})
	end := bytes.LastIndex(data, []byte{'P', 'K', 5, 6})
	if end < 0 {
		t.Fatal("fixture has no ZIP end record")
	}
	central := int(binary.LittleEndian.Uint32(data[end+16 : end+20]))
	// Signed APK-style packages insert an opaque block immediately before the
	// central directory and update its offset, retaining local-entry offsets.
	// This tests that ZIP layout, not any SDK signature format or trust chain.
	payload := []byte("opaque signature fixture")
	block := make([]byte, 8+8+4+len(payload)+8+16)
	binary.LittleEndian.PutUint64(block[:8], uint64(len(block)-8))
	binary.LittleEndian.PutUint64(block[8:16], uint64(4+len(payload)))
	binary.LittleEndian.PutUint32(block[16:20], 0x12345678)
	copy(block[20:], payload)
	binary.LittleEndian.PutUint64(block[len(block)-24:len(block)-16], uint64(len(block)-8))
	copy(block[len(block)-16:], "APK Sig Block 42")
	withBlock := append([]byte{}, data[:central]...)
	withBlock = append(withBlock, block...)
	withBlock = append(withBlock, data[central:]...)
	binary.LittleEndian.PutUint32(withBlock[end+len(block)+16:end+len(block)+20], uint32(central+len(block)))
	archive, err := zip.NewReader(bytes.NewReader(withBlock), int64(len(withBlock)))
	if err != nil || len(archive.File) != 1 {
		t.Fatalf("Go zip cannot read package with signing block: %v", err)
	}
	stream, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	metadata, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || string(metadata) != projectPackageTestMetadata {
		t.Fatalf("Go zip could not read metadata before signing block: %q %v %v", metadata, readErr, closeErr)
	}
	for _, extension := range []string{".hap", ".hsp"} {
		filename := projectTestFile(t, t.TempDir(), "package"+extension, withBlock)
		if err := checkProjectEmbeddedMetadata(context.Background(), filename, "org.oheco.exportfixture", 12); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectEmbeddedCopyBounds(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 64<<10)
	for _, tc := range []struct {
		name string
		size uint64
	}{
		{"exact-limit", uint64(len(payload))},
		{"declared-over-limit", uint64(len(payload) + 1)},
		{"underdeclared", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := projectEmbeddedZIP(t, projectEmbeddedZIPEntry{name: "module.json", data: payload, raw: true, size: tc.size, crc: crc32.ChecksumIEEE(payload)})
			archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			n, err := copyProjectEmbeddedEntry(context.Background(), &output, archive.File[0], int64(len(payload)))
			if tc.name == "exact-limit" {
				if err != nil || n != int64(len(payload)) || !bytes.Equal(output.Bytes(), payload) {
					t.Fatalf("bounded copy failed at size limit: %d %v", n, err)
				}
			} else if err == nil || n > int64(len(payload)+1) || (tc.name == "declared-over-limit" && n != 0) {
				t.Fatalf("accepted invalid length or exceeded copy bound: %d %v", n, err)
			}
		})
	}
}

type projectEmbeddedCancelWriter struct{ cancel context.CancelFunc }

func (w projectEmbeddedCancelWriter) Write(data []byte) (int, error) {
	w.cancel()
	return len(data), nil
}

func TestProjectEmbeddedCopyCancellation(t *testing.T) {
	data := projectEmbeddedZIP(t, projectEmbeddedZIPEntry{name: "payload", data: bytes.Repeat([]byte("x"), 2<<20)})
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	n, err := copyProjectEmbeddedEntry(ctx, &output, archive.File[0], 2<<20)
	if !errors.Is(err, context.Canceled) || n != 0 || output.Len() != 0 {
		t.Fatalf("canceled copy consumed data: %d %v", n, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	n, err = copyProjectEmbeddedEntry(ctx, projectEmbeddedCancelWriter{cancel}, archive.File[0], 2<<20)
	if !errors.Is(err, context.Canceled) || n >= 2<<20 || n <= 0 {
		t.Fatalf("stream did not stop promptly after cancellation: %d %v", n, err)
	}
}

func TestProjectEmbeddedContextReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := bytes.NewReader([]byte("xy"))
	reader := projectEmbeddedContextReader{ctx, source}
	var data [1]byte
	if n, err := reader.Read(data[:]); n != 1 || err != nil || data[0] != 'x' {
		t.Fatalf("reader did not forward data: %d %v %q", n, err, data)
	}
	cancel()
	if n, err := reader.Read(data[:]); n != 0 || !errors.Is(err, context.Canceled) || source.Len() != 1 {
		t.Fatalf("canceled reader consumed data: %d %v remaining=%d", n, err, source.Len())
	}
	reader = projectEmbeddedContextReader{context.Background(), bytes.NewReader(nil)}
	if n, err := reader.Read(data[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("reader did not forward EOF: %d %v", n, err)
	}
}
