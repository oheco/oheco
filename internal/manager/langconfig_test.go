package manager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNpmScopeProbeArguments(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options LanguageOptions
		want    []string
	}{
		{"default", LanguageOptions{}, []string{"--global"}},
		{"bare nil", LanguageOptions{HasBackendArgs: true}, nil},
		{"bare empty", LanguageOptions{HasBackendArgs: true, Args: []string{}}, nil},
		{"logging only", LanguageOptions{HasBackendArgs: true, Args: []string{"--loglevel", "verbose", "--json"}}, nil},
		{"global false", LanguageOptions{HasBackendArgs: true, Args: []string{"--global", "false"}}, []string{"--global", "false"}},
		{"short global false", LanguageOptions{HasBackendArgs: true, Args: []string{"-g=false"}}, []string{"-g=false"}},
		{"ordered scope", LanguageOptions{HasBackendArgs: true, Args: []string{"--global", "--no-global", "--location=global", "--prefix", "/with spaces", "-C=/second", "--local"}}, []string{"--global", "--no-global", "--location=global", "--prefix", "/with spaces", "-C=/second", "--local"}},
		{"short cluster", LanguageOptions{HasBackendArgs: true, Args: []string{"-sg"}}, []string{"--global"}},
		{"short cluster global false", LanguageOptions{HasBackendArgs: true, Args: []string{"-sg=false"}}, []string{"--global=false"}},
		{"short cluster assignment not global", LanguageOptions{HasBackendArgs: true, Args: []string{"-gs=false"}}, []string{"--global"}},
		{"single dash logging", LanguageOptions{HasBackendArgs: true, Args: []string{"-loglevel=verbose", "-ignore-scripts"}}, nil},
		{"single dash scope", LanguageOptions{HasBackendArgs: true, Args: []string{"-global=false", "-location=global", "-prefix=/with spaces"}}, []string{"--global=false", "--location=global", "--prefix=/with spaces"}},
		{"short location", LanguageOptions{HasBackendArgs: true, Args: []string{"-L", "global"}}, []string{"-L", "global"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := npmScopeArgs(tc.options); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("scope=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultPipScopeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, config, command, conflict string
	}{
		{"global user", "global.user='true'", "install", "global.user"},
		{"global user uninstall", "global.user='true'", "uninstall", "global.user"},
		{"install target", "install.target='/private target'", "install", "install.target"},
		{"global prefix", "global.prefix='/private prefix'", "install", "global.prefix"},
		{"global root", "global.root='/private root'", "install", "global.root"},
		{"uninstall python", "uninstall.python='/another python'", "uninstall", "uninstall.python"},
		{"global python", "global.python='/another python'", "install", "global.python"},
		{"global python cannot be overridden by command", "global.python='/another python'\ninstall.python=''", "install", "global.python"},
		{"env target", ":env:.target='/private target'", "install", "PIP_TARGET"},
		{"ambiguous user", "global.user='not-a-boolean'", "install", "global.user"},
		{"false user", "global.user='false'", "install", ""},
		{"empty destination", "global.prefix=''", "install", ""},
		{"command overrides global", "global.user='true'\ninstall.user='false'", "install", ""},
		{"empty command user falls back", "global.user='true'\ninstall.user=''", "install", "global.user"},
		{"empty command prefix falls back", "global.prefix='/private prefix'\ninstall.prefix=''", "install", "global.prefix"},
		{"env overrides global", "global.user='true'\n:env:.user='false'", "install", ""},
		{"unrelated command", "download.target='/private target'\ninstall.prefix='/private prefix'", "uninstall", ""},
		{"source and logging", "global.index-url='https://pypi.org/simple'\nglobal.timeout='10'", "install", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, log := languageTestManager(t, "pip")
			python := filepath.Join(os.Getenv("PATH"), "python3")
			languageTestPythonConfig(t, python, languagePythonInfo{Executable: python, BaseExecutable: python, Prefix: "/base", BasePrefix: "/base"}, true, tc.config)
			_, err := m.operationLanguageTool(context.Background(), "pip", tc.command, LanguageOptions{})
			if tc.conflict == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.conflict) || !strings.Contains(err.Error(), "use --") {
				t.Fatalf("destination override not rejected: %v", err)
			}
			for _, args := range [][]string{nil, {}, {"--verbose"}} {
				if _, err := m.operationLanguageTool(context.Background(), "pip", tc.command, LanguageOptions{Args: args, HasBackendArgs: true}); err != nil {
					t.Fatalf("explicit -- did not opt out: %v", err)
				}
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("scope check mutated environment: %v", err)
			}
		})
	}
}

// A persistent global.python must be discovered before entering pip's CLI:
// otherwise even --version/config list can execute the redirected interpreter.
func TestDefaultPipScopeRejectsPersistentInterpreterRedirectBeforeCLI(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	if err := exec.Command(python, "-B", "-c", "from pip._internal.configuration import Configuration").Run(); err != nil {
		t.Skip("pip configuration loader unavailable")
	}
	m, _, _ := languageTestManager(t, "pip")
	dir := t.TempDir()
	redirect, marker, config := filepath.Join(dir, "redirected python"), filepath.Join(dir, "redirect marker"), filepath.Join(dir, "pip.conf")
	languageTestExecutable(t, redirect, "printf 'redirected\\n' > \"$OHECO_TEST_REDIRECT_MARKER\"\n")
	if err := os.WriteFile(config, []byte("[global]\npython = "+redirect+"\n[install]\npython =\n[uninstall]\npython =\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("PIP_CONFIG_FILE", config)
	t.Setenv("OHECO_PYTHON", python)
	t.Setenv("OHECO_TEST_REDIRECT_MARKER", marker)
	for _, command := range []string{"install", "uninstall"} {
		_, err := m.operationLanguageTool(context.Background(), "pip", command, LanguageOptions{})
		if err == nil || !strings.Contains(err.Error(), "global.python") {
			t.Fatalf("persistent interpreter redirect not rejected: %v", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("redirected interpreter executed before scope check: %v", err)
		}
	}
}

// Exercise the official npm config loader, without installing packages or
// changing user/global config. HOME, prefix and project files are all isolated.
func TestNpmUpstreamMatchesOfficialScopeConfig(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm unavailable")
	}
	dir := t.TempDir()
	home, project, prefix := filepath.Join(dir, "home"), filepath.Join(dir, "project"), filepath.Join(dir, "prefix")
	for _, path := range []string{home, project, filepath.Join(prefix, "etc")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(project, "package.json"): `{"name":"scope-fixture","version":"1.0.0"}`,
		filepath.Join(home, ".npmrc"):          "registry=https://user.example.invalid/\n@user:registry=https://user-scope.example.invalid/\n",
		filepath.Join(project, ".npmrc"):       "registry=https://project.example.invalid/\n@project:registry=https://project-scope.example.invalid/\n//project.example.invalid/:_authToken=private-fixture-token\n",
	}
	for path, contents := range files {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Error(err)
		}
	})
	var env []string
	for _, entry := range languageEnv("npm") {
		key, _, _ := strings.Cut(entry, "=")
		if strings.ToUpper(key) != "HOME" && !strings.HasPrefix(strings.ToUpper(key), "NPM_CONFIG_") {
			env = append(env, entry)
		}
	}
	env = append(env, "HOME="+home, "NPM_CONFIG_PREFIX="+prefix)
	m := &Manager{}
	tool := languageToolchain{backend: "npm", program: npm, env: env}
	for _, tc := range []struct {
		name    string
		options LanguageOptions
		project bool
	}{
		{"default global", LanguageOptions{}, false},
		{"bare separator local", LanguageOptions{HasBackendArgs: true}, true},
		{"logging local", LanguageOptions{HasBackendArgs: true, Args: []string{"--loglevel=verbose"}}, true},
		{"explicit global", LanguageOptions{HasBackendArgs: true, Args: []string{"--global"}}, false},
		{"explicit global false", LanguageOptions{HasBackendArgs: true, Args: []string{"--global=false"}}, true},
		{"global location", LanguageOptions{HasBackendArgs: true, Args: []string{"--location=global"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, err := m.npmUpstream(context.Background(), tool, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			want := "https://user.example.invalid/"
			if tc.project {
				want = "https://project.example.invalid/"
			}
			if upstream.Registry != want {
				t.Fatalf("registry=%q, want %q", upstream.Registry, want)
			}
			_, hasProject := upstream.Scoped["@project"]
			if hasProject != tc.project {
				t.Fatalf("project scope incorrectly loaded: %v", upstream.Scoped)
			}
			if len(upstream.Auth) != 0 && !tc.project {
				t.Fatalf("project credentials reported in global mode: %q", upstream.Auth)
			}
			if tc.project && !reflect.DeepEqual(upstream.Auth, []string{"project.example.invalid"}) {
				t.Fatalf("local credential hint missing: %q", upstream.Auth)
			}
		})
	}
}
