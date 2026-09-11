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
	archives := flag.String("archives", "dist", "directory containing signed oheco archives")
	flag.Parse()
	for _, filename := range []string{*archives, filepath.Join(*site, "install.sh")} {
		if _, err := os.Stat(filename); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/artifacts/", http.StripPrefix("/artifacts/", http.FileServer(http.Dir(*archives))))
	mux.Handle("/", http.FileServer(http.Dir(*site)))
	fmt.Println("Serving native test fixtures at http://127.0.0.1:18808")
	if err := http.ListenAndServe("127.0.0.1:18808", mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
