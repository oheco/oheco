package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

func TestUnifiedLanguageOptionSafety(t *testing.T) {
	bad := map[string][][]string{
		"npm": {{"--registry=https://evil.invalid"}, {"--reg=https://evil.invalid"}, {"--@scope:registry=https://evil.invalid"}, {"--userconfig", "npmrc"}, {"--ignore-scripts=false"}, {"--no-ignore-scripts"}, {"--script-shell=/tmp/script"}, {"--workspace=other"}, {"-wother"}, {"--prefix", "--registry=https://evil.invalid"}, {"other-root"}, {"https://evil.invalid/a.tgz"}, {"/tmp/archive.tgz"}, {"--", "other-root"}},
		"pip": {{"--index-url=https://evil.invalid"}, {"--extra-index=https://evil.invalid"}, {"--find-links=./wheels"}, {"--no-index"}, {"--no-binary=:all:"}, {"--only-binary=:none:"}, {"--isolated"}, {"--python=/other/python"}, {"-r", "requirements.txt"}, {"-rrequirements.txt"}, {"-vrrequirements.txt"}, {"-e."}, {"-Cbuild=x"}, {"--constraint=constraints.txt"}, {"--target", "--extra-index-url=https://evil.invalid"}, {"other-root"}, {"pkg @ https://evil.invalid/pkg.whl"}, {"./pkg.whl"}, {"--", "other-root"}},
	}
	for backend, cases := range bad {
		for _, args := range cases {
			if err := validateLanguageOptions(backend, args, false); err == nil {
				t.Errorf("%s accepted %q", backend, args)
			}
		}
	}
}

func TestUnifiedLanguageOptionsPreserveArgv(t *testing.T) {
	good := map[string][][]string{
		"npm": {{"--global=false"}, {"--global"}, {"-g"}, {"--no-global"}, {"--location=project"}, {"--location", "global"}, {"--prefix", "/a path/with spaces"}, {"--prefix=/a path"}, {"--include", "optional"}, {"--unknown-ordinary-option"}, {"--unknown-ordinary-option=one two"}, {"--json", "--depth=0"}},
		"pip": {{"--target", "/a path/with spaces"}, {"--prefix=/a path"}, {"--user", "--upgrade"}, {"--no-deps", "--no-cache-dir"}, {"--python-version=3.13"}, {"--unknown-ordinary-option"}, {"--unknown-ordinary-option=one two"}, {"--format=json"}},
	}
	for backend, cases := range good {
		for _, args := range cases {
			before := append([]string{}, args...)
			if err := validateLanguageOptions(backend, args, false); err != nil {
				t.Errorf("%s rejected %q: %v", backend, args, err)
			}
			argv := languageCommand(languageToolchain{backend: backend}, "install", []string{"root"}, args, true, "http://127.0.0.1/nonce", "/tmp/config")
			if !reflect.DeepEqual(before, args) || !languageContainsSequence(argv, args) {
				t.Errorf("modified argv: before=%q after=%q command=%q", before, args, argv)
			}
		}
	}
}

func TestUnifiedNpmScopeDefaults(t *testing.T) {
	for _, tc := range []struct {
		args             []string
		global, explicit bool
	}{
		{nil, true, false},
		{[]string{"--global=false"}, false, true},
		{[]string{"--location=project"}, false, true},
		{[]string{"--prefix", "/tmp/global with spaces"}, true, false},
		{[]string{"--prefix=/tmp/global with spaces"}, true, false},
		{[]string{"--global=false", "--prefix", "/tmp/local with spaces"}, false, true},
		{[]string{"--global", "--prefix", "/tmp/global with spaces"}, true, true},
		{[]string{"-g"}, true, true},
		{[]string{"--no-global"}, false, true},
		{[]string{"--location", "global"}, true, true},
		{[]string{"--global=false", "--global=true"}, true, true},
	} {
		global, explicit, _, err := npmScope(tc.args, true)
		if err != nil || global != tc.global || explicit != tc.explicit {
			t.Errorf("scope %q = %v,%v,%v", tc.args, global, explicit, err)
		}
		argv := languageCommand(languageToolchain{backend: "npm"}, "install", []string{"root@1.0.0"}, tc.args, true, "http://127.0.0.1", "/tmp/config")
		n := 0
		for _, a := range argv {
			if a == "--global" {
				n++
			}
		}
		want := 0
		if !tc.explicit {
			want++
		}
		for _, a := range tc.args {
			if a == "--global" {
				want++
			}
		}
		if n != want {
			t.Errorf("unexpected inserted global flag in %q", argv)
		}
	}
	argv := languageCommand(languageToolchain{backend: "npm"}, "install", []string{"root"}, nil, false, "http://127.0.0.1", "/tmp/config")
	if languageContainsSequence(argv, []string{"--global"}) {
		t.Fatal("changed legacy npm local default")
	}
}

func TestUnifiedLanguageExactTargets(t *testing.T) {
	for _, tc := range []struct {
		backend, target string
		install, valid  bool
	}{
		{"npm", "pkg@1.2.3", true, true}, {"npm", "@scope/pkg@1.2.3-beta.1", true, true},
		{"npm", "pkg@latest", true, false}, {"npm", "pkg@^1.0.0", true, false},
		{"npm", "file:/tmp/a", true, false}, {"npm", "pkg", true, true}, {"npm", "@scope/pkg", true, true}, {"pip", "pkg", true, true},
		{"npm", "@scope/pkg", false, true}, {"npm", "pkg@1.0.0", false, false},
		{"pip", "some_pkg==1.2.0+local", true, true}, {"pip", "pkg==1.*", true, false},
		{"pip", "pkg>=1.0", true, false}, {"pip", "pkg===1.0", true, false},
		{"pip", "pkg @ https://evil.invalid/a.whl", true, false}, {"pip", "pkg", false, true},
		{"pip", "pkg==1.0", false, false},
	} {
		err := validateLanguageTargets(tc.backend, []string{tc.target}, tc.install)
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

// Tests use shell stand-ins, not the user's npm/pip environment. Only version
// probes run in dry-run/preflight; mutation commands are recorded, never run.
func languageTestExecutable(t *testing.T, path, body string) {
	t.Helper()
	shell := ""
	for _, candidate := range []string{"/usr/bin/zsh", "/bin/sh", "/usr/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			shell = candidate
			break
		}
	}
	if shell == "" {
		t.Skip("no shell for isolated backend fixtures")
	}
	if strings.HasSuffix(shell, "/zsh") {
		shell += " -f"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!"+shell+"\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
}

func languageTestPython(t *testing.T, path string, info languagePythonInfo, pip bool) {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	version := "printf 'pip fixture\\n'; exit 0"
	if !pip {
		version = "printf 'No module named pip\\n' >&2; exit 7"
	}
	body := "if [ \"$2\" = '-c' ]; then printf '%s\\n' '" + strings.ReplaceAll(string(data), "'", "'\\''") + "'; exit 0; fi\n" +
		"case \"$*\" in *--version*) " + version + " ;; esac\n" +
		"printf '%s\\n' \"$@\" > \"$OHECO_TEST_LANGUAGE_ARGS\"\n"
	languageTestExecutable(t, path, body)
}

func languageTestManager(t *testing.T, backend string) (*Manager, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tools")
	log := filepath.Join(dir, "backend-argv")
	var out bytes.Buffer
	m, err := New(filepath.Join(dir, "oo absent"), "https://index.invalid/index.json", &out)
	if err != nil {
		t.Fatal(err)
	}
	m.In = strings.NewReader("")
	m.Client = &http.Client{Transport: languageNoNetwork{t}}
	if backend == "npm" {
		languageTestExecutable(t, filepath.Join(bin, "node"), "printf 'v22.0.0\\n'\n")
		languageTestExecutable(t, filepath.Join(bin, "npm"), "case \"$*\" in *--version*) printf '10.0.0\\n'; exit 0 ;; esac\nfor a in \"$@\"; do if [ \"$a\" = '--unknown-ordinary-option' ]; then printf 'fixture backend unknown option\\n'; exit 23; fi; done\nprintf '%s\\n' \"$@\" > \"$OHECO_TEST_LANGUAGE_ARGS\"\n")
	} else {
		python := filepath.Join(bin, "python3")
		languageTestPython(t, python, languagePythonInfo{Executable: python, BaseExecutable: python, Prefix: bin, BasePrefix: bin}, true)
	}
	t.Setenv("PATH", bin)
	t.Setenv("OHECO_PYTHON", "")
	t.Setenv("OHECO_TEST_LANGUAGE_ARGS", log)
	return m, &out, log
}

type languageNoNetwork struct{ t *testing.T }

func (tr languageNoNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.t.Errorf("unexpected network request: %s", r.URL)
	return nil, fmt.Errorf("network forbidden in test")
}

func languageTestIndex() catalog.Index {
	return catalog.Index{SchemaVersion: 3, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Packages: []catalog.Package{}}
}

func languageReadArgv(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func languageContainsSequence(argv, seq []string) bool {
	for i := 0; i+len(seq) <= len(argv); i++ {
		if reflect.DeepEqual(argv[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

func TestUnifiedLanguageInstallOwnsNoState(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		t.Run(backend, func(t *testing.T) {
			m, out, log := languageTestManager(t, backend)
			specs, args := []string{"root@1.0.0", "second@2.0.0"}, []string{"--unknown-ordinary-option=value with spaces"}
			if backend == "pip" {
				specs = []string{"root==1.0.0", "second==2.0.0"}
			}
			if err := m.InstallLanguage(context.Background(), languageTestIndex(), backend, specs, LanguageOptions{Args: args, Yes: true}); err != nil {
				t.Fatal(err)
			}
			argv := languageReadArgv(t, log)
			if !languageContainsSequence(argv, append(specs, args...)) {
				t.Fatalf("changed root/options argv: %q", argv)
			}
			if backend == "npm" {
				if !languageContainsSequence(argv, []string{"--global"}) || !languageContainsSequence(argv, []string{"--ignore-scripts"}) {
					t.Fatalf("missing npm defaults: %q", argv)
				}
			} else if !languageContainsSequence(argv, []string{"--only-binary=:all:"}) || languageContainsSequence(argv, []string{"--global"}) {
				t.Fatalf("incorrect pip defaults: %q", argv)
			}
			if !strings.Contains(out.String(), "NOT a complete dependency plan") {
				t.Fatal("claimed an uncomputed dependency plan")
			}
			if _, err := os.Stat(filepath.Join(m.Root, "state")); !os.IsNotExist(err) {
				t.Fatalf("created state: %v", err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(m.Root, "tmp", "npmrc-*"))
			if len(leftovers) != 0 {
				t.Fatalf("left temporary config: %v", leftovers)
			}
		})
	}
}

func TestUnifiedLanguageRemoveOffline(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		t.Run(backend, func(t *testing.T) {
			m, out, log := languageTestManager(t, backend)
			if err := m.RemoveLanguage(context.Background(), backend, []string{"root", "second"}, LanguageOptions{Yes: true}); err != nil {
				t.Fatal(err)
			}
			argv := languageReadArgv(t, log)
			for _, a := range argv {
				if strings.Contains(a, "--registry") || strings.Contains(a, "--index-url") {
					t.Fatalf("registry on removal: %q", argv)
				}
			}
			if backend == "npm" && !languageContainsSequence(argv, []string{"--offline"}) {
				t.Fatal("npm removal could access remote registry")
			}
			if backend == "pip" && (!languageContainsSequence(argv, []string{"--yes"}) || !strings.Contains(out.String(), "dependencies are retained")) {
				t.Fatal("pip did not preserve dependencies/confirm once")
			}
			if !strings.Contains(out.String(), "reverse dependency safety is not guaranteed") {
				t.Fatal("missing reverse dependency boundary")
			}
			if _, err := os.Stat(filepath.Join(m.Root, "index")); !os.IsNotExist(err) {
				t.Fatalf("loaded index: %v", err)
			}
			if _, err := os.Stat(filepath.Join(m.Root, "state")); !os.IsNotExist(err) {
				t.Fatalf("wrote state: %v", err)
			}
		})
	}
}

func TestUnifiedLanguageDryRunAndPreflightNoChanges(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		t.Run(backend, func(t *testing.T) {
			m, out, log := languageTestManager(t, backend)
			if err := m.CheckLanguage(context.Background(), backend, nil); err != nil {
				t.Fatal(err)
			}
			if out.Len() != 0 {
				t.Fatal("preflight printed a duplicate plan")
			}
			spec := "root@1.0.0"
			if backend == "pip" {
				spec = "root==1.0.0"
			}
			if err := m.InstallLanguage(context.Background(), catalog.Index{}, backend, []string{spec}, LanguageOptions{DryRun: true}); err != nil {
				t.Fatal(err)
			}
			if err := m.RemoveLanguage(context.Background(), backend, []string{"root"}, LanguageOptions{DryRun: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("dry run executed a mutation: %v", err)
			}
			if _, err := os.Stat(m.Root); !os.IsNotExist(err) {
				t.Fatalf("dry run created environment: %v", err)
			}
			if !strings.Contains(out.String(), "NOT a complete dependency plan") || !strings.Contains(out.String(), "Backend argv:") {
				t.Fatalf("incomplete dry run output: %s", out)
			}
		})
	}
}

func TestUnifiedLanguageConfirmationDefaultsNo(t *testing.T) {
	for _, remove := range []bool{false, true} {
		m, _, log := languageTestManager(t, "npm")
		m.In = strings.NewReader("\n")
		var err error
		if remove {
			err = m.RemoveLanguage(context.Background(), "npm", []string{"root"}, LanguageOptions{})
		} else {
			err = m.InstallLanguage(context.Background(), catalog.Index{}, "npm", []string{"root@1.0.0"}, LanguageOptions{})
		}
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("default confirmation did not reject: %v", err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("mutated before confirmation: %v", err)
		}
	}
}

func TestUnifiedUnknownOptionReportedByBackend(t *testing.T) {
	m, out, _ := languageTestManager(t, "npm")
	err := m.InstallLanguage(context.Background(), languageTestIndex(), "npm", []string{"root@1.0.0"}, LanguageOptions{Args: []string{"--unknown-ordinary-option"}, Yes: true})
	if err == nil || !strings.Contains(out.String(), "fixture backend unknown option") {
		t.Fatalf("unknown option never reached backend: %v; %s", err, out)
	}
}

func TestUnifiedMissingToolchains(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		t.Run(backend, func(t *testing.T) {
			m, _, _ := languageTestManager(t, backend)
			t.Setenv("PATH", t.TempDir())
			err := m.CheckLanguage(context.Background(), backend, nil)
			var missing *MissingToolchainError
			want := "nodejs"
			if backend == "pip" {
				want = "python3"
			}
			if !errors.As(err, &missing) || missing.Package != want || missing.Reason == "" {
				t.Fatalf("missing %s: %v", backend, err)
			}
		})
	}
	t.Run("python without pip", func(t *testing.T) {
		m, _, _ := languageTestManager(t, "pip")
		python := filepath.Join(os.Getenv("PATH"), "python3")
		languageTestPython(t, python, languagePythonInfo{Executable: python, BaseExecutable: python, Prefix: "/base", BasePrefix: "/base"}, false)
		err := m.CheckLanguage(context.Background(), "pip", nil)
		var missing *MissingToolchainError
		if !errors.As(err, &missing) || missing.Package != "python3" || !strings.Contains(missing.Reason, "-m pip") {
			t.Fatalf("missing pip: %v", err)
		}
	})
	t.Run("node without npm", func(t *testing.T) {
		m, _, _ := languageTestManager(t, "npm")
		if err := os.Remove(filepath.Join(os.Getenv("PATH"), "npm")); err != nil {
			t.Fatal(err)
		}
		err := m.CheckLanguage(context.Background(), "npm", nil)
		var missing *MissingToolchainError
		if !errors.As(err, &missing) || missing.Package != "nodejs" {
			t.Fatalf("missing npm: %v", err)
		}
	})
}

func TestUnifiedPythonEnvironmentSelection(t *testing.T) {
	m, _, _ := languageTestManager(t, "pip")
	pathPython := filepath.Join(os.Getenv("PATH"), "python3")
	base := filepath.Join(t.TempDir(), "base python")
	languageTestPython(t, base, languagePythonInfo{Executable: base, BaseExecutable: base, Prefix: "/base", BasePrefix: "/base"}, true)
	languageTestPython(t, pathPython, languagePythonInfo{Executable: pathPython, BaseExecutable: base, Prefix: "/activated-venv", BasePrefix: "/base"}, true)
	t.Setenv("VIRTUAL_ENV", "/activated-venv")
	tool, err := m.languageTool(context.Background(), "pip")
	if err != nil || tool.program != base || !strings.Contains(tool.note, "virtualenv bypassed") {
		t.Fatalf("did not bypass activated venv: program=%s note=%s err=%v", tool.program, tool.note, err)
	}
	active := filepath.Join(m.Root, "bin", "python3")
	languageTestPython(t, active, languagePythonInfo{Executable: active, BaseExecutable: active, Prefix: "/oheco", BasePrefix: "/oheco"}, true)
	tool, err = m.languageTool(context.Background(), "pip")
	if err != nil || tool.program != active {
		t.Fatalf("did not prefer oheco active Python: program=%s err=%v", tool.program, err)
	}
	t.Setenv("OHECO_PYTHON", pathPython)
	tool, err = m.languageTool(context.Background(), "pip")
	if err != nil || tool.program != pathPython || !strings.Contains(tool.note, "explicitly selected virtualenv") {
		t.Fatalf("did not respect explicit venv: program=%s note=%s err=%v", tool.program, tool.note, err)
	}
	t.Setenv("OHECO_PYTHON", filepath.Join(t.TempDir(), "missing-python"))
	_, err = m.languageTool(context.Background(), "pip")
	var missing *MissingToolchainError
	if !errors.As(err, &missing) {
		t.Fatalf("silently fell back from explicit Python: %v", err)
	}
}

func TestUnifiedCancelledProbeNotMissingToolchain(t *testing.T) {
	m, _, _ := languageTestManager(t, "npm")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.CheckLanguage(ctx, "npm", nil)
	var missing *MissingToolchainError
	if !errors.Is(err, context.Canceled) || errors.As(err, &missing) {
		t.Fatalf("cancelled probe was misclassified: %v", err)
	}
}

func TestLegacyLanguageRemovalDoesNotLoadIndex(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		t.Run(backend, func(t *testing.T) {
			m, _, log := languageTestManager(t, backend)
			if err := m.Language(context.Background(), backend, []string{"uninstall", "root"}); err != nil {
				t.Fatal(err)
			}
			argv := languageReadArgv(t, log)
			if languageContainsSequence(argv, []string{"--global"}) || languageContainsSequence(argv, []string{"--yes"}) {
				t.Fatalf("changed legacy scope/confirmation: %q", argv)
			}
		})
	}
}

func TestUnifiedNpmProjectPreflightBeforeToolchain(t *testing.T) {
	m, out, _ := languageTestManager(t, "npm")
	t.Setenv("PATH", t.TempDir()) // The project policy must win over missing npm.
	parent := t.TempDir()
	nested := filepath.Join(parent, "nested", "child")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".npmrc"), []byte("@scope:registry=https://private.invalid/secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{nested, filepath.Join(nested, "not-created")} {
		err := m.CheckLanguageInstall(context.Background(), "npm", []string{"--global=false", "--prefix", prefix})
		var missing *MissingToolchainError
		if err == nil || errors.As(err, &missing) || !strings.Contains(err.Error(), "can bypass oo") {
			t.Fatalf("project preflight: %v", err)
		}
		if strings.Contains(err.Error(), "private.invalid") || out.Len() != 0 {
			t.Fatal("logged project configuration value")
		}
	}
	link := filepath.Join(t.TempDir(), "linked-prefix")
	if err := os.Symlink(nested, link); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckLanguageInstall(context.Background(), "npm", []string{"--global=false", "--prefix", link}); err == nil || !strings.Contains(err.Error(), "can bypass oo") {
		t.Fatalf("missed physical parent config: %v", err)
	}
}

func TestUnifiedCompatibleProtectionFlags(t *testing.T) {
	for _, args := range [][]string{{"--global", "false"}, {"--no-audit"}, {"--ignore-scripts"}, {"--ignore-scripts", "true"}, {"--ignore-scripts=true"}, {"--audit=false"}, {"--no-fund"}} {
		if err := validateLanguageOptions("npm", args, false); err != nil {
			t.Errorf("rejected compatible safety option %q: %v", args, err)
		}
	}
	if err := validateLanguageOptions("pip", []string{"--only-binary=:all:"}, false); err != nil {
		t.Fatal(err)
	}
	if err := validateLanguageOptions("npm", []string{"--ignore-scripts", "false"}, true); err == nil {
		t.Fatal("accepted disabling scripts via separate false")
	}
	global, explicit, _, err := npmScope([]string{"--global", "false"}, true)
	if err != nil || global || !explicit {
		t.Fatal("separate global=false override misidentified")
	}
	for _, args := range [][]string{{"--loc=project"}, {"--pref=/tmp"}, {"-gw=other"}, {"-C/other"}} {
		if err := validateLanguageOptions("npm", args, false); err == nil {
			t.Errorf("accepted unchecked scope shorthand %q", args)
		}
	}
}

func TestLanguageEnvironmentStripsRegistryOverrides(t *testing.T) {
	for k, v := range map[string]string{"NPM_CONFIG_REGISTRY": "https://evil.invalid", "npm_config_ignore_scripts": "false", "PIP_EXTRA_INDEX_URL": "https://evil.invalid", "HTTP_PROXY": "http://evil.invalid", "https_proxy": "http://evil.invalid", "NODE_OPTIONS": "--require=/tmp/script.js"} {
		t.Setenv(k, v)
	}
	for _, backend := range []string{"npm", "pip"} {
		for _, entry := range languageEnv(backend) {
			if strings.Contains(entry, "evil.invalid") || strings.Contains(entry, "script.js") {
				t.Fatalf("leaked override: %s", entry)
			}
		}
	}
}
