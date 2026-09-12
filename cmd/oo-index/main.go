// oo-index validates package descriptors and builds the static publication tree.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/manager"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "oo-index:", err)
		os.Exit(1)
	}
}
func run() error {
	directory := flag.String("packages", "../oheco-packages/packages", "package descriptor directory")
	output := flag.String("output", "../oheco-packages/public", "output directory")
	site := flag.String("site", "../oheco-packages/site", "static website directory")
	template := flag.String("installer-template", "scripts/install.sh.tmpl", "zsh installer template")
	verify := flag.Bool("verify-artifacts", false, "download artifacts and verify their hashes before publishing")
	inspect := flag.String("inspect", "", "read language artifact metadata from a local wheel or npm tarball")
	packageManager := flag.String("package-manager", "", "pip or npm for --inspect")
	artifactURL := flag.String("url", "", "immutable download URL for --inspect")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *inspect != "" {
		a, err := catalog.Inspect(*packageManager, *inspect, *artifactURL)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(a)
	}
	files, err := filepath.Glob(filepath.Join(*directory, "*.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no package descriptors found")
	}
	idx := catalog.Index{SchemaVersion: catalog.SchemaVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Packages: []catalog.Package{}}
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		var p catalog.Package
		err = catalog.Decode(f, &p)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if filepath.Base(file) != p.Name+".json" {
			return fmt.Errorf("%s: filename must match package name", file)
		}
		idx.Packages = append(idx.Packages, p)
	}
	sort.Slice(idx.Packages, func(i, j int) bool { return idx.Packages[i].Name < idx.Packages[j].Name })
	if err := idx.Validate(); err != nil {
		return err
	}
	if *verify {
		client := manager.HTTPClient()
		for _, p := range idx.Packages {
			for _, v := range p.Versions {
				for _, a := range v.PipArtifacts {
					if err := verifyLanguage(client, "pip", a.File, a); err != nil {
						return err
					}
				}
				if a := v.NpmArtifacts; a != nil {
					if err := verifyLanguage(client, "npm", a.File, *a); err != nil {
						return err
					}
				}
				for platform, a := range v.Artifacts {
					fmt.Printf("Verifying %s@%s %s\n", p.Name, v.Version, platform)
					resp, err := client.Get(a.URL)
					if err != nil {
						return err
					}
					if resp.StatusCode != 200 {
						resp.Body.Close()
						return fmt.Errorf("%s: %s", a.URL, resp.Status)
					}
					h := sha256.New()
					n, err := io.Copy(h, io.LimitReader(resp.Body, a.Size+1))
					resp.Body.Close()
					if err != nil {
						return err
					}
					if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
						return fmt.Errorf("artifact size/hash mismatch: %s", a.URL)
					}
				}
			}
		}
	}
	p, err := idx.Find("oheco")
	if err != nil {
		return err
	}
	version, a, err := p.Resolve("", "ohos-arm64")
	if err != nil {
		return err
	}
	if a.StripComponents != 0 || a.Binaries["oo"] != "bin/oo" {
		return fmt.Errorf("oheco bootstrap artifact must contain bin/oo at the archive root")
	}
	tmpl, err := os.ReadFile(*template)
	if err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	installer := strings.NewReplacer("@@VERSION@@", quote(version), "@@URL@@", quote(a.URL), "@@SHA256@@", quote(a.SHA256), "@@MANIFEST@@", string(manifest)).Replace(string(tmpl))
	if err := writeIndexes(*output, idx); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "install.sh"), []byte(installer), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, ".nojekyll"), nil, 0644); err != nil {
		return err
	}
	if err := filepath.WalkDir(*site, func(filename string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(*site, filename)
		if err != nil {
			return err
		}
		target := filepath.Join(*output, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("site contains a non-regular file: %s", filename)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	}); err != nil {
		return err
	}
	fmt.Printf("Built %s: %d packages; bootstrap oheco %s\n", *output, len(idx.Packages), version)
	return nil
}

func writeIndexes(output string, idx catalog.Index) error {
	for schema := 1; schema <= idx.SchemaVersion; schema++ {
		index := idx.Compatible(schema)
		if err := index.Validate(); err != nil {
			return err
		}
		dir := filepath.Join(output, "index", fmt.Sprintf("v%d", index.SchemaVersion))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		data, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "index.json"), append(data, '\n'), 0644); err != nil {
			return err
		}
	}
	return nil
}
