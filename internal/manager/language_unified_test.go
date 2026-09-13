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
			if err := validateLanguageOptions(backend, args); err == nil {
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
			if err := validateLanguageOptions(backend, args); err != nil {
				t.Errorf("%s rejected %q: %v", backend, args, err)
			}
			argv := languageCommand(languageToolchain{backend: backend}, "install", []string{"root"}, args, "http://127.0.0.1/nonce", nil)
			if !reflect.DeepEqual(before, args) || !languageContainsSequence(argv, args) {
				t.Errorf("modified argv: before=%q after=%q command=%q", before, args, argv)
			}
		}
	}
}

// TestUnifiedLanguageInjectedArgvExactly pins the complete set of arguments oo
// adds. Nothing else may be injected: no scope override, no audit/fund/retry
// policy, and no legacy-only flags.
func TestUnifiedLanguageInjectedArgvExactly(t *testing.T) {
	const url = "http://127.0.0.1:43210"
	scopes := []string{"@scope"}
	for _, tc := range []struct {
		name             string
		backend, command string
		targets, args    []string
		registryURL      string
		scopes           []string
		want             []string
	}{
		{
			name: "npm install", backend: "npm", command: "install",
			targets: []string{"root@1.0.0"}, args: []string{"--unknown-ordinary-option=value"},
			registryURL: url, scopes: scopes,
			want: []string{"install", "root@1.0.0", "--unknown-ordinary-option=value", "--ignore-scripts", "--omit-lockfile-registry-resolved", "--replace-registry-host=never",
				"--registry=" + url + "/npm/", "--@scope:registry=" + url + "/npm/"},
		},
		{
			name: "npm uninstall", backend: "npm", command: "uninstall",
			targets: []string{"root"},
			want:    []string{"uninstall", "root", "--ignore-scripts", "--offline"},
		},
		{
			name: "pip install", backend: "pip", command: "install",
			targets: []string{"root==1.0.0"}, registryURL: url,
			want: []string{"-B", "-m", "pip", "install", "root==1.0.0", "--index-url=" + url + "/simple/", "--only-binary=:all:"},
		},
		{
			name: "pip uninstall", backend: "pip", command: "uninstall",
			targets: []string{"root"},
			want:    []string{"-B", "-m", "pip", "uninstall", "root", "--yes"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := languageCommand(languageToolchain{backend: tc.backend}, tc.command, tc.targets, tc.args, tc.registryURL, tc.scopes)
			if !reflect.DeepEqual(argv, tc.want) {
				t.Fatalf("argv = %q, want %q", argv, tc.want)
			}
			for _, removed := range []string{"--global", "--no-audit", "--no-fund", "--no-update-notifier", "--fetch-retries=0"} {
				if languageContainsSequence(argv, []string{removed}) {
					t.Fatalf("argv still injects %s: %q", removed, argv)
				}
			}
		})
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
	languageTestPythonConfig(t, path, info, pip, "")
}

// languageTestPythonConfig builds a Python stand-in that also answers the
// `-B -m pip config list` probe runLanguage issues before starting its
// temporary index. configList is the raw output of that probe.
func languageTestPythonConfig(t *testing.T, path string, info languagePythonInfo, pip bool, configList string) {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	version := "printf 'pip fixture\\n'; exit 0"
	if !pip {
		version = "printf 'No module named pip\\n' >&2; exit 7"
	}
	quote := func(s string) string { return strings.ReplaceAll(s, "'", "'\\''") }
	body := "if [ \"$2\" = '-c' ]; then printf '%s\\n' '" + quote(string(data)) + "'; exit 0; fi\n" +
		"case \"$*\" in\n" +
		"*\"pip config list\"*) printf '%s\\n' '" + quote(configList) + "'; exit 0 ;;\n" +
		"*--version*) " + version + " ;;\n" +
		"esac\n" +
		"printf '%s\\n' \"$@\" > \"$OHECO_TEST_LANGUAGE_ARGS\"\n"
	languageTestExecutable(t, path, body)
}

// languageTestNpm builds an npm stand-in. The config probes mirror the real
// `npm config get registry` / `npm config ls --json` calls runLanguage makes to
// learn the user's effective sources; registry and configJSON empty use the
// public defaults. Mutation commands record their argv, never run.
func languageTestNpm(t *testing.T, path, registry, configJSON string) {
	t.Helper()
	if registry == "" {
		registry = "https://registry.npmjs.org/"
	}
	if configJSON == "" {
		configJSON = "{}"
	}
	quote := func(s string) string { return strings.ReplaceAll(s, "'", "'\\''") }
	body := "case \"$*\" in\n" +
		"*--version*) printf '10.0.0\\n'; exit 0 ;;\n" +
		"\"config get registry\") printf '%s\\n' '" + quote(registry) + "'; exit 0 ;;\n" +
		"\"config ls --json\") printf '%s\\n' '" + quote(configJSON) + "'; exit 0 ;;\n" +
		"esac\n" +
		"for a in \"$@\"; do if [ \"$a\" = '--unknown-ordinary-option' ]; then printf 'fixture backend unknown option\\n'; exit 23; fi; done\n" +
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
		languageTestNpm(t, filepath.Join(bin, "npm"), "", "")
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
				if languageContainsSequence(argv, []string{"--global"}) {
					t.Fatalf("npm install must not force a scope: %q", argv)
				}
				for _, flag := range []string{"--ignore-scripts", "--omit-lockfile-registry-resolved", "--replace-registry-host=never"} {
					if !languageContainsSequence(argv, []string{flag}) {
						t.Fatalf("missing npm policy flag %s: %q", flag, argv)
					}
				}
				for _, removed := range []string{"--no-audit", "--no-fund", "--no-update-notifier", "--fetch-retries=0"} {
					if languageContainsSequence(argv, []string{removed}) {
						t.Fatalf("npm install still injects %s: %q", removed, argv)
					}
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
			if backend == "npm" && (!languageContainsSequence(argv, []string{"--offline"}) || !languageContainsSequence(argv, []string{"--ignore-scripts"})) {
				t.Fatalf("npm removal could access remote registry or run scripts: %q", argv)
			}
			if languageContainsSequence(argv, []string{"--global"}) {
				t.Fatalf("removal must not force a scope: %q", argv)
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

func TestUnifiedCompatibleProtectionFlags(t *testing.T) {
	for _, args := range [][]string{{"--global", "false"}, {"--global"}, {"-g"}, {"--no-global"}, {"--no-audit"}, {"--ignore-scripts"}, {"--ignore-scripts", "true"}, {"--ignore-scripts=true"}, {"--audit=false"}, {"--no-fund"}, {"--fetch-retries=0"}} {
		if err := validateLanguageOptions("npm", args); err != nil {
			t.Errorf("rejected compatible safety option %q: %v", args, err)
		}
	}
	if err := validateLanguageOptions("pip", []string{"--only-binary=:all:"}); err != nil {
		t.Fatal(err)
	}
	if err := validateLanguageOptions("npm", []string{"--ignore-scripts", "false"}); err == nil {
		t.Fatal("accepted disabling scripts via separate false")
	}
	for _, args := range [][]string{{"--loc=project"}, {"--pref=/tmp"}, {"-gw=other"}, {"-C/other"}} {
		if err := validateLanguageOptions("npm", args); err == nil {
			t.Errorf("accepted unchecked scope shorthand %q", args)
		}
	}
	// Scope flags pass through untouched: oo neither inserts nor rewrites them.
	argv := languageCommand(languageToolchain{backend: "npm"}, "install", []string{"root@1.0.0"}, []string{"--global"}, "", nil)
	if !languageContainsSequence(argv, []string{"--global"}) {
		t.Fatalf("dropped a user-supplied scope flag: %q", argv)
	}
}

func TestLanguageEnvironmentPreservesUserConfig(t *testing.T) {
	// oo no longer owns the download path, so it stops shadowing the user's
	// registries, indexes and proxy. Only code-injection tokens are dropped and
	// loopback must bypass a configured proxy for oo's temporary source.
	preserved := map[string]string{
		"NPM_CONFIG_REGISTRY":       "https://registry.example.invalid",
		"npm_config_ignore_scripts": "false",
		"PIP_EXTRA_INDEX_URL":       "https://index.example.invalid/simple",
		"PIP_CONFIG_FILE":           "/tmp/user-pip.conf",
		"HTTP_PROXY":                "http://proxy.example.invalid",
		"https_proxy":               "http://proxy.example.invalid",
		"NPM_CONFIG_PROXY":          "http://proxy.example.invalid",
		"NO_PROXY":                  "corp.example.invalid",
	}
	for k, v := range preserved {
		t.Setenv(k, v)
	}
	t.Setenv("NODE_OPTIONS", "--require=/tmp/script.js")
	for _, backend := range []string{"npm", "pip"} {
		env := languageEnv(backend)
		values := map[string]string{}
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			values[key] = value
		}
		for key, value := range preserved {
			if key == "NO_PROXY" {
				continue
			}
			if values[key] != value {
				t.Errorf("%s dropped user %s=%q (got %q)", backend, key, value, values[key])
			}
		}
		if _, ok := values["NODE_OPTIONS"]; ok {
			t.Errorf("%s kept NODE_OPTIONS", backend)
		}
		for _, key := range []string{"NO_PROXY", "no_proxy"} {
			bypass := values[key]
			if !strings.Contains(bypass, "127.0.0.1") || !strings.Contains(bypass, "localhost") || !strings.Contains(bypass, "corp.example.invalid") {
				t.Errorf("%s %s=%q does not preserve the proxy bypass", backend, key, bypass)
			}
		}
	}
}

// TestUnifiedNpmForwardsOwnRegistryWithoutConfigInjection pins the new npm
// contract: oo still points npm at its synthesized catalog, but it no longer
// injects --userconfig/--globalconfig to shadow the user's real npmrc.
func TestUnifiedNpmForwardsOwnRegistryWithoutConfigInjection(t *testing.T) {
	m, _, log := languageTestManager(t, "npm")
	idx := languageTestIndex()
	idx.Packages = append(idx.Packages, catalog.Package{
		SchemaVersion: 3, Name: "adapted", PackageManager: "npm", PackageName: "@scope/adapted",
		Description: "adapted fixture", Upstream: "https://example.com/adapted",
		Repository: "https://github.com/oheco/adapted", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT",
		Latest: map[string]string{m.Platform: "1.0.0"},
		Versions: []catalog.Version{{Version: "1.0.0", NpmArtifacts: &catalog.NpmArtifact{
			File:        catalog.File{URL: "https://example.com/adapted-1.0.0.tgz", SHA256: strings.Repeat("a", 64), Size: 1, Filename: "adapted-1.0.0.tgz"},
			PackageJSON: json.RawMessage(`{"name":"@scope/adapted","version":"1.0.0"}`),
		}}},
	})
	if err := m.InstallLanguage(context.Background(), idx, "npm", []string{"@scope/adapted@1.0.0"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	argv := languageReadArgv(t, log)
	for _, a := range argv {
		if a == "--userconfig" || a == "--globalconfig" || strings.HasPrefix(a, "--userconfig=") || strings.HasPrefix(a, "--globalconfig=") {
			t.Fatalf("oo injected its own config file: %q", argv)
		}
	}
	var registryArg string
	for _, a := range argv {
		if strings.HasPrefix(a, "--registry=") {
			registryArg = strings.TrimPrefix(a, "--registry=")
		}
	}
	if !strings.Contains(registryArg, "127.0.0.1") || !strings.HasSuffix(registryArg, "/npm/") {
		t.Fatalf("missing temporary registry override: %q", argv)
	}
	if !languageContainsSequence(argv, []string{"--@scope:registry=" + registryArg}) {
		t.Fatalf("missing catalog scope override: %q", argv)
	}
	for _, flag := range []string{"--ignore-scripts", "--omit-lockfile-registry-resolved", "--replace-registry-host=never"} {
		if !languageContainsSequence(argv, []string{flag}) {
			t.Fatalf("missing npm policy flag %s: %q", flag, argv)
		}
	}
	for _, removed := range []string{"--global", "--no-audit", "--no-fund", "--no-update-notifier", "--fetch-retries=0"} {
		if languageContainsSequence(argv, []string{removed}) {
			t.Fatalf("unexpected injected flag %s: %q", removed, argv)
		}
	}
}

// TestUnifiedAnonymousUpstreamWarning checks that credentials found in the
// user's npm configuration produce an explicit anonymous-access warning and
// that the secret itself is never echoed. npm hides credential values from
// `config ls`, so oo scans the npmrc key names instead.
func TestUnifiedAnonymousUpstreamWarning(t *testing.T) {
	m, out, _ := languageTestManager(t, "npm")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".npmrc"),
		[]byte("//registry.example.invalid/:_authToken=fake-token-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := m.InstallLanguage(context.Background(), languageTestIndex(), "npm", []string{"root@1.0.0"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "registry.example.invalid") || !strings.Contains(out.String(), "anonymous") {
		t.Fatalf("missing anonymous warning: %s", out)
	}
	if strings.Contains(out.String(), "fake-token-value") {
		t.Fatal("leaked credential value")
	}
}

// TestUnifiedPipFindLinksRejected verifies that a find-links source, which oo
// cannot aggregate, fails with an explicit error instead of being ignored.
func TestUnifiedPipFindLinksRejected(t *testing.T) {
	m, _, _ := languageTestManager(t, "pip")
	python := filepath.Join(os.Getenv("PATH"), "python3")
	languageTestPythonConfig(t, python, languagePythonInfo{Executable: python, BaseExecutable: python, Prefix: os.Getenv("PATH"), BasePrefix: os.Getenv("PATH")}, true, "global.find-links = ./wheels\n")
	err := m.InstallLanguage(context.Background(), languageTestIndex(), "pip", []string{"root==1.0.0"}, LanguageOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "find-links") {
		t.Fatalf("find-links not reported: %v", err)
	}
}

// TestUnifiedPipUpstreamAggregatesConfiguredIndexes checks the effective pip
// configuration is read back, including the `:env:.` prefix pip uses for
// environment-provided values.
func TestUnifiedPipUpstreamAggregatesConfiguredIndexes(t *testing.T) {
	m, _, _ := languageTestManager(t, "pip")
	python := filepath.Join(os.Getenv("PATH"), "python3")
	languageTestPythonConfig(t, python, languagePythonInfo{Executable: python, BaseExecutable: python, Prefix: os.Getenv("PATH"), BasePrefix: os.Getenv("PATH")}, true,
		"global.index-url = https://primary.example.invalid/simple\n:env:.extra-index-url = https://extra.example.invalid/simple\n")
	tool, err := m.languageTool(context.Background(), "pip")
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := m.pipUpstream(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://primary.example.invalid/simple", "https://extra.example.invalid/simple"}
	if !reflect.DeepEqual(upstream.Indexes, want) {
		t.Fatalf("indexes = %q, want %q", upstream.Indexes, want)
	}
}

func TestParsePipConfigHandlesEnvAndQuotes(t *testing.T) {
	output := strings.Join([]string{
		"global.index-url='https://pypi.org/simple'",
		":env:.extra-index-url=https://mirror.example.invalid/simple",
		"global.extra-index-url = \"https://second.example.invalid/simple\"",
		"global.find-links = ./wheels",
		"global.timeout = 10",
		"install.no-input = true",
	}, "\n")
	primary, extras, findLinks := parsePipConfig(output)
	if !reflect.DeepEqual(primary, []string{"https://pypi.org/simple"}) {
		t.Fatalf("primary = %q", primary)
	}
	if !reflect.DeepEqual(extras, []string{"https://mirror.example.invalid/simple", "https://second.example.invalid/simple"}) {
		t.Fatalf("extras = %q", extras)
	}
	if !reflect.DeepEqual(findLinks, []string{"./wheels"}) {
		t.Fatalf("findLinks = %q", findLinks)
	}
}
