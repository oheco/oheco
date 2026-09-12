package main

import "testing"

func TestExportArguments(t *testing.T) {
	for _, tc := range []struct {
		args            []string
		spec, name, out string
	}{
		{[]string{"godot"}, "godot", "", ""},
		{[]string{"godot@1", "editor", "-o", "a b"}, "godot@1", "editor", "a b"},
		{[]string{"--output=out", "godot", "editor"}, "godot", "editor", "out"},
		{[]string{"godot", "--output", "out"}, "godot", "", "out"},
	} {
		spec, name, out, err := exportArgs(tc.args)
		if err != nil || spec != tc.spec || name != tc.name || out != tc.out {
			t.Fatalf("%v: %s %s %s %v", tc.args, spec, name, out, err)
		}
	}
	for _, args := range [][]string{nil, {"godot", "editor", "extra"}, {"godot", "--output"}, {"godot", "--output="}, {"godot", "-o", "a", "--output", "b"}, {"godot", "--force"}} {
		if _, _, _, err := exportArgs(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
