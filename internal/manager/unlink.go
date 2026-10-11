package manager

import (
	"context"
	"fmt"

	"github.com/oheco/oheco/internal/catalog"
)

// Unlink releases default and versioned command names without changing package
// files or receipts. An explicit install/switch can enable the commands again.
func (m *Manager) Unlink(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("usage: oo unlink <native-package>...")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.withLock(func() error {
		before, err := m.LoadState()
		if err != nil {
			return err
		}
		after := cloneState(before)
		for _, name := range names {
			if !catalog.ValidName(name) || name == "oheco" {
				return fmt.Errorf("cannot unlink %q: specify an installed native package other than oheco", name)
			}
			p, exists := after.Packages[name]
			if !exists {
				return fmt.Errorf("%s is not an installed native package; npm/pip command ownership is managed by their backends", name)
			}
			p.CommandsDisabled = true
			after.Packages[name] = p
		}
		after.SchemaVersion = 3
		if err := after.validate(); err != nil {
			return err
		}
		if err := m.checkActiveClient(ctx, after, nil); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.commit(before, after, "", ""); err != nil {
			return err
		}
		for _, name := range names {
			p := after.Packages[name]
			fmt.Fprintf(m.Out, "Unlinked %s; installed files and versions retained", name)
			if p.Active != "" {
				fmt.Fprintf(m.Out, "; restore with oo switch %s %s", name, p.Active)
			}
			fmt.Fprintln(m.Out)
		}
		return nil
	})
}
