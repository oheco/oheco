package main

import (
	"reflect"
	"testing"
)

func TestOperationArgsMultipleTargetsAndLiteralTail(t *testing.T) {
	args := []string{"git", "--yes", "deepseek-harness@0.1.5-rc.2-ohos.2", "--", "--prefix", "/tmp/with spaces", "--some-option=a;b", "--", "-y"}
	specs, install, _, err := operationArgs("install", args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(specs, []string{"git", "deepseek-harness@0.1.5-rc.2-ohos.2"}) {
		t.Fatalf("specs=%#v", specs)
	}
	if !install.Yes || !install.HasBackendArgs || !reflect.DeepEqual(install.Args, args[4:]) {
		t.Fatalf("tail rewritten: %#v", install)
	}
}

func TestOperationArgsRemoveSafetyFlags(t *testing.T) {
	specs, _, remove, err := operationArgs("remove", []string{"a", "b", "--all", "--autoremove", "--cascade", "--dry-run", "-y"})
	if err != nil || len(specs) != 2 || !remove.All || !remove.AutoRemove || !remove.Cascade || !remove.DryRun || !remove.Yes {
		t.Fatalf("%#v %#v %v", specs, remove, err)
	}
	_, _, remove, err = operationArgs("remove", []string{"a", "-y"})
	if err != nil || remove.AutoRemove || remove.Cascade {
		t.Fatal("--yes implied destructive flags")
	}
}

func TestOperationArgsExplicitSeparator(t *testing.T) {
	for _, command := range []string{"install", "remove"} {
		for _, tc := range []struct {
			name     string
			args     []string
			provided bool
			want     []string
		}{
			{"absent", []string{"npm:root"}, false, nil},
			{"bare", []string{"npm:root", "--"}, true, []string{}},
			{"logging only", []string{"npm:root", "--", "--loglevel=verbose"}, true, []string{"--loglevel=verbose"}},
			{"scope", []string{"npm:root", "--", "--global=false"}, true, []string{"--global=false"}},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				_, install, remove, err := operationArgs(command, tc.args)
				if err != nil {
					t.Fatal(err)
				}
				if install.HasBackendArgs != tc.provided || remove.HasBackendArgs != tc.provided {
					t.Fatalf("separator not recorded: install=%+v remove=%+v", install, remove)
				}
				if !reflect.DeepEqual(install.Args, tc.want) || !reflect.DeepEqual(remove.Args, tc.want) {
					t.Fatalf("tail changed: install=%+v remove=%+v", install, remove)
				}
			})
		}
	}
}

func TestOperationArgsRejectAmbiguousOrEmptyInput(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"install", nil}, {"remove", []string{"--", "a"}}, {"install", []string{"a", "-g"}}, {"install", []string{"a", "--all"}},
		{"remove", []string{"a", "--no-switch"}}, {"install", []string{"a", "--yes", "-y"}},
	} {
		if _, _, _, err := operationArgs(tc.command, tc.args); err == nil {
			t.Fatalf("accepted %s %#v", tc.command, tc.args)
		}
	}
}
