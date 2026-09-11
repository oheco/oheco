package catalog

import (
	"strings"
	"testing"
)

func TestURLPolicy(t *testing.T) {
	for _, s := range []string{"https://github.com/oheco/go", "http://127.0.0.1:1234/index.json", "http://[::1]/index"} {
		if err := ValidateURL(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"http://github.com/oheco/go", "file:///etc/passwd", "https://user:password@github.com/file", "http://127.0.0.1.example.com/index"} {
		if ValidateURL(s) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestPaths(t *testing.T) {
	for _, s := range []string{"../bin/go", "/bin/go", "bin/../go", "bin//go", "bin\\go", "."} {
		if SafePath(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"bin/go", "go", "pkg/tool/ohos_arm64/compile"} {
		if !SafePath(s) {
			t.Fatalf("rejected %q", s)
		}
	}
}
func TestDecodeRejectsUnknownAndTrailingData(t *testing.T) {
	for _, s := range []string{`{"schema_version":1,"typo":true}`, `{"schema_version":1} {}`} {
		var idx Index
		if Decode(strings.NewReader(s), &idx) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
