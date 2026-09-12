package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

func TestVerifyPublishedProjectBytesAndPaths(t *testing.T) {
	for _, name := range []string{"app/build-profile.json5", "../escape"} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		w, _ := z.Create(name)
		_, _ = w.Write([]byte("{}"))
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(b.Bytes()) }))
		project := catalog.Project{URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(b.Bytes())), Size: int64(b.Len()), Format: "zip", StripComponents: 1}
		err := verifyProject(server.Client(), project)
		if name[0] == '.' && err == nil {
			t.Fatal("published traversal archive accepted")
		}
		if name[0] != '.' && err != nil {
			t.Fatal(err)
		}
		project.Size++
		if verifyProject(server.Client(), project) == nil {
			t.Fatal("wrong project size accepted")
		}
		server.Close()
	}
}
