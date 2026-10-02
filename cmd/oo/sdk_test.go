package main

import "testing"

func TestSDKArgs(t *testing.T) {
	for _, command := range []string{"create", "path", "remove"} {
		got, value, err := sdkArgs([]string{command, "26.0.0.35.Beta"})
		if err != nil || got != command || value != "26.0.0.35.Beta" {
			t.Fatalf("%s: %s %s %v", command, got, value, err)
		}
	}
	if got, value, err := sdkArgs([]string{"list"}); err != nil || got != "list" || value != "" {
		t.Fatalf("list: %s %s %v", got, value, err)
	}
	for _, args := range [][]string{nil, {"create"}, {"path"}, {"remove"}, {"list", "extra"}, {"create", "v", "extra"}, {"unknown", "v"}} {
		if _, _, err := sdkArgs(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
