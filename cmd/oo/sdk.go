package main

import (
	"context"
	"fmt"

	"github.com/oheco/oheco/internal/manager"
)

func sdkArgs(args []string) (string, string, error) {
	if len(args) == 1 && args[0] == "list" {
		return "list", "", nil
	}
	if len(args) == 2 {
		switch args[0] {
		case "create", "path", "remove":
			return args[0], args[1], nil
		}
	}
	return "", "", fmt.Errorf("usage: oo sdk create <installed-package-version> | list | path <view-id> | remove <view-id>")
}

func runSDK(ctx context.Context, m *manager.Manager, args []string) error {
	command, value, err := sdkArgs(args)
	if err != nil {
		return err
	}
	switch command {
	case "create":
		view, err := m.SDKCreate(ctx, value)
		if err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "SDK %s: %s\n", view.ID, view.Root)
	case "list":
		views, err := m.SDKList()
		if err != nil {
			return err
		}
		for _, view := range views {
			fmt.Fprintf(m.Out, "%s\t%s\n", view.ID, view.Root)
		}
	case "path":
		root, err := m.SDKPath(value)
		if err != nil {
			return err
		}
		fmt.Fprintln(m.Out, root)
	case "remove":
		if err := m.SDKRemove(ctx, value); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "Removed SDK view %s; component packages retained\n", value)
	}
	return nil
}
