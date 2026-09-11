// Local fixture server for the native end-to-end test. Not part of oo releases.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	site := flag.String("site", "../oheco-packages/tmp/e2e-public", "local fixture site")
	ooArchive := flag.String("oheco", "dist/oheco-0.1.0-ohos-arm64.tar.gz", "signed oheco archive")
	goArchive := flag.String("go", "../dist/go1.27.1.ohos-arm64.tar.gz", "signed Go archive")
	flag.Parse()
	for _, filename := range []string{*ooArchive, *goArchive, filepath.Join(*site, "install.sh")} {
		if _, err := os.Stat(filename); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/artifacts/oheco.tar.gz", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, *ooArchive) })
	mux.HandleFunc("/artifacts/go.tar.gz", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, *goArchive) })
	mux.Handle("/", http.FileServer(http.Dir(*site)))
	fmt.Println("Serving native test fixtures at http://127.0.0.1:18808")
	if err := http.ListenAndServe("127.0.0.1:18808", mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
