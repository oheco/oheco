package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanguageDefaultScopeArgv(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		for _, command := range []string{"install", "uninstall"} {
			for _, tc := range []struct {
				name         string
				options      LanguageOptions
				defaultScope bool
			}{
				{"absent nil", LanguageOptions{}, true},
				{"absent empty", LanguageOptions{Args: []string{}}, true},
				{"bare nil", LanguageOptions{HasBackendArgs: true}, false},
				{"bare empty", LanguageOptions{HasBackendArgs: true, Args: []string{}}, false},
				{"logging only", LanguageOptions{HasBackendArgs: true, Args: []string{"--verbose"}}, false},
			} {
				t.Run(backend+"/"+command+"/"+tc.name, func(t *testing.T) {
					argv := languageCommand(languageToolchain{backend: backend}, command, []string{"root"}, tc.options, "http://127.0.0.1/nonce", nil)
					global := languageContainsSequence(argv, []string{"--global"})
					noUser := languageContainsSequence(argv, []string{"--no-user"})
					if global != (backend == "npm" && tc.defaultScope) || noUser != (backend == "pip" && command == "install" && tc.defaultScope) {
						t.Fatalf("incorrect default scope: %q", argv)
					}
					for _, arg := range argv {
						if arg == "--user" || strings.HasPrefix(arg, "--prefix") || strings.HasPrefix(arg, "--target") {
							t.Fatalf("invented destination: %q", argv)
						}
					}
					if len(tc.options.Args) != 0 && !languageContainsSequence(argv, tc.options.Args) {
						t.Fatalf("lost explicit arguments: %q", argv)
					}
				})
			}
		}
	}
}

func TestLanguageScopeFlagRequiredForArguments(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		m, _, log := languageTestManager(t, backend)
		_, err := m.operationLanguageTool(context.Background(), backend, "install", LanguageOptions{Args: []string{"--verbose"}})
		if err == nil || !strings.Contains(err.Error(), "HasBackendArgs") {
			t.Fatalf("scope inferred from slice length for %s: %v", backend, err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("unexpected mutation: %v", err)
		}
	}
}

func TestLanguageTailPresenceValidation(t *testing.T) {
	for _, tc := range []struct {
		groups   map[string][]string
		provided bool
		args     []string
		valid    bool
	}{
		{nil, false, nil, true},
		{nil, true, nil, false},
		{map[string][]string{"npm": {"root"}}, true, nil, true},
		{map[string][]string{"npm": {"root"}}, true, []string{}, true},
		{map[string][]string{"npm": {"root"}, "pip": {"root"}}, true, nil, false},
		{map[string][]string{"npm": {"root"}}, false, []string{"--verbose"}, false},
	} {
		if err := validateTail(tc.groups, tc.provided, tc.args); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestLanguageBatchPropagatesScope(t *testing.T) {
	for _, backend := range []string{"npm", "pip"} {
		for _, tc := range []struct {
			name     string
			provided bool
			args     []string
		}{
			{"default", false, nil},
			{"bare separator", true, nil},
			{"logging only", true, []string{"--verbose"}},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				m, out, log := languageTestManager(t, backend)
				if err := m.prepare(); err != nil {
					t.Fatal(err)
				}
				idx, err := json.Marshal(languageTestIndex())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(m.Root, "index", "index.json"), idx, 0644); err != nil {
					t.Fatal(err)
				}
				spec := backend + ":root"
				if err := m.InstallMany(context.Background(), []string{spec}, InstallOptions{Args: tc.args, HasBackendArgs: tc.provided, Yes: true}); err != nil {
					t.Fatal(err)
				}
				argv := languageReadArgv(t, log)
				if languageContainsSequence(argv, []string{"--global"}) != (backend == "npm" && !tc.provided) || languageContainsSequence(argv, []string{"--no-user"}) != (backend == "pip" && !tc.provided) {
					t.Fatalf("install lost scope flag: %q", argv)
				}
				if !strings.Contains(out.String(), languageScopeDescription(backend, tc.provided)) {
					t.Fatalf("plan scope missing: %s", out)
				}
				if err := m.RemoveMany(context.Background(), []string{spec}, RemoveOptions{Args: tc.args, HasBackendArgs: tc.provided, Yes: true}); err != nil {
					t.Fatal(err)
				}
				argv = languageReadArgv(t, log)
				if languageContainsSequence(argv, []string{"--global"}) != (backend == "npm" && !tc.provided) || languageContainsSequence(argv, []string{"--no-user"}) {
					t.Fatalf("remove lost scope or used nonexistent pip option: %q", argv)
				}
			})
		}
	}
}

func TestDefaultPipScopeRejectsDestinationEnvironment(t *testing.T) {
	for _, key := range []string{"PIP_USER", "PIP_TARGET", "PIP_PREFIX", "PIP_ROOT", "PIP_PYTHON", "PYTHONPATH", "PYTHONHOME"} {
		for _, command := range []string{"install", "uninstall"} {
			t.Run(key+"/"+command, func(t *testing.T) {
				m, _, log := languageTestManager(t, "pip")
				value := "private-destination"
				if key == "PIP_USER" {
					value = "true"
				}
				t.Setenv(key, value)
				_, err := m.operationLanguageTool(context.Background(), "pip", command, LanguageOptions{})
				if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "use --") {
					t.Fatalf("override not rejected: %v", err)
				}
				if strings.Contains(err.Error(), value) {
					t.Fatalf("configuration value leaked: %v", err)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("mutation during scope check: %v", err)
				}
				for _, args := range [][]string{nil, {}, {"--verbose"}} {
					tool, err := m.operationLanguageTool(context.Background(), "pip", command, LanguageOptions{Args: args, HasBackendArgs: true})
					if err != nil {
						t.Fatalf("explicit -- did not opt out: %v", err)
					}
					if !languageContainsSequence(tool.env, []string{key + "=" + value}) || languageContainsSequence(tool.env, []string{"PYTHONNOUSERSITE=1"}) {
						t.Fatalf("explicit environment changed: %q", tool.env)
					}
				}
			})
		}
	}
}

func TestDefaultPipScopeDisablesUserSite(t *testing.T) {
	m, _, _ := languageTestManager(t, "pip")
	for _, user := range []string{"", "0", "false", "no", "off"} {
		t.Setenv("PIP_USER", user)
		for _, command := range []string{"install", "uninstall"} {
			tool, err := m.operationLanguageTool(context.Background(), "pip", command, LanguageOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !languageContainsSequence(tool.env, []string{"PYTHONNOUSERSITE=1"}) {
				t.Fatalf("user site still visible: %q", tool.env)
			}
		}
	}
}

func TestDefaultPipScopeUnwritableDoesNotFallback(t *testing.T) {
	m, _, log := languageTestManager(t, "pip")
	python := filepath.Join(os.Getenv("PATH"), "python3")
	info, err := json.Marshal(languagePythonInfo{Executable: python, BaseExecutable: python, Prefix: "/unwritable-base", BasePrefix: "/unwritable-base"})
	if err != nil {
		t.Fatal(err)
	}
	languageTestExecutable(t, python, "case \"$3\" in *\"_oheco_pip_config\"*) printf '{}\\n'; exit 0 ;; esac\n"+
		"if [ \"$2\" = '-c' ]; then printf '%s\\n' '"+string(info)+"'; exit 0; fi\n"+
		"case \"$*\" in *--version*) printf 'pip fixture\\n'; exit 0 ;; *\"pip config list\"*) exit 0 ;; esac\n"+
		"printf '%s\\n' \"$@\" > \"$OHECO_TEST_LANGUAGE_ARGS\"\n"+
		"for a in \"$@\"; do if [ \"$a\" = '--no-user' ]; then printf 'global site-packages is not writable\\n'; exit 13; fi; done\n"+
		"printf 'user fallback performed\\n' >> \"$OHECO_TEST_LANGUAGE_ARGS\"\n")
	err = m.InstallLanguage(context.Background(), languageTestIndex(), "pip", []string{"root==1.0.0"}, LanguageOptions{Yes: true})
	if err == nil {
		t.Fatal("unwritable global install fell back instead of failing")
	}
	argv := languageReadArgv(t, log)
	if !languageContainsSequence(argv, []string{"--no-user"}) || languageContainsSequence(argv, []string{"user fallback performed"}) {
		t.Fatalf("user fallback allowed: %q", argv)
	}
}

func TestDefaultPipScopeRespectsExplicitVenv(t *testing.T) {
	m, _, _ := languageTestManager(t, "pip")
	python := filepath.Join(t.TempDir(), "explicit python")
	languageTestPython(t, python, languagePythonInfo{Executable: python, BaseExecutable: "/base/python", Prefix: "/explicit-venv", BasePrefix: "/base"}, true)
	t.Setenv("OHECO_PYTHON", python)
	tool, err := m.operationLanguageTool(context.Background(), "pip", "install", LanguageOptions{})
	if err != nil || tool.program != python || !strings.Contains(tool.note, "explicitly selected virtualenv") {
		t.Fatalf("explicit venv replaced: %+v %v", tool, err)
	}
}
