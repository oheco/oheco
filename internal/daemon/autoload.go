package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	autoloadStartMarker = "# >>> oheco daemon autoload >>>"
	autoloadEndMarker   = "# <<< oheco daemon autoload <<<"
	// Remember an inserted separator so uninstall can restore a file that did
	// not originally end in a newline.
	autoloadSeparatorMarker = "# oheco daemon autoload: preceding newline added"
)

type autoloadBlock struct {
	start          int
	end            int
	addedSeparator bool
	finalNewline   bool
}

// Autoload installs, removes, or prints the daemon's zsh startup block. The root
// must be the caller's absolute, canonical OHECO_ROOT. Only print writes to out;
// installing and removing an unchanged block are silent and do not rewrite it.
func Autoload(root string, mode string, out io.Writer) error {
	if mode == "" {
		mode = "install"
	}
	if mode != "install" && mode != "remove" && mode != "print" {
		return fmt.Errorf("invalid daemon autoload mode %q; use install, remove, or print", mode)
	}
	if !filepath.IsAbs(root) || strings.IndexByte(root, 0) >= 0 {
		return fmt.Errorf("daemon autoload requires an absolute OHECO_ROOT")
	}

	if mode != "remove" {
		executable := filepath.Join(root, "bin", "oo")
		info, err := os.Stat(executable)
		if err != nil {
			return fmt.Errorf("daemon autoload requires the stable oo executable %q: %w", executable, err)
		}
		// HOME's hmdfs does not expose executable mode bits reliably. Require
		// the stable file to exist, rather than rejecting a working oo there.
		if !info.Mode().IsRegular() {
			return fmt.Errorf("daemon autoload stable oo executable %q is not a regular file", executable)
		}
	}
	if mode == "print" {
		if out == nil {
			out = io.Discard
		}
		_, err := io.WriteString(out, autoloadScript(root, false))
		return err
	}

	dir := os.Getenv("ZDOTDIR")
	if dir == "" {
		var err error
		dir, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate zsh startup file: %w", err)
		}
	}
	filename, err := filepath.Abs(filepath.Join(dir, ".zshrc"))
	if err != nil {
		return err
	}
	dir = filepath.Dir(filename)
	if mode == "remove" {
		if _, err := os.Lstat(filename); os.IsNotExist(err) {
			return nil
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if os.IsNotExist(err) {
		if info, e := os.Lstat(filename); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to edit a dangling zsh startup symlink: %s", filename)
		}
		parent, e := filepath.EvalSymlinks(dir)
		if e != nil {
			return e
		}
		resolved = filepath.Join(parent, ".zshrc")
	} else if err != nil {
		return err
	}
	if info, err := os.Stat(resolved); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("zsh startup file is not a regular file: %s", filename)
	}
	lock, err := waitLock(context.Background(), resolved+".oheco-daemon.lock", 5*time.Second)
	if err != nil {
		return err
	}
	defer lock.Close()
	originalInfo, infoErr := os.Lstat(resolved)
	if infoErr != nil && !os.IsNotExist(infoErr) {
		return infoErr
	}
	if originalInfo != nil && !originalInfo.Mode().IsRegular() {
		return fmt.Errorf("zsh startup target is not a regular file: %s", filename)
	}
	original, err := os.ReadFile(filename)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read zsh startup file %q: %w", filename, err)
	}
	if os.IsNotExist(err) && mode == "remove" {
		return nil
	}
	block, err := autoloadFindBlock(original)
	if err != nil {
		return fmt.Errorf("refusing to edit zsh startup file %q: %w", filename, err)
	}

	var updated []byte
	if block == nil {
		if mode == "remove" {
			return nil
		}
		separator := len(original) != 0 && original[len(original)-1] != '\n'
		updated = append(updated, original...)
		if separator {
			updated = append(updated, '\n')
		}
		updated = append(updated, autoloadScript(root, separator)...)
	} else if mode == "remove" {
		start := block.start
		if block.addedSeparator {
			start--
		}
		updated = append(updated, original[:start]...)
		updated = append(updated, original[block.end:]...)
	} else {
		script := autoloadScript(root, block.addedSeparator)
		if !block.finalNewline {
			script = strings.TrimSuffix(script, "\n")
		}
		updated = append(updated, original[:block.start]...)
		updated = append(updated, script...)
		updated = append(updated, original[block.end:]...)
	}
	if bytes.Equal(original, updated) {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create zsh configuration directory %q: %w", dir, err)
	}
	// Commit to the resolved target, retaining the user's symlink and file mode.
	// The fixed sibling is a persistent edit journal; a failed/interrupted write
	// never truncates the current zsh configuration.
	info := originalInfo
	modeBits := os.FileMode(0600)
	if info != nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("zsh startup target changed: %s", filename)
		}
		modeBits = info.Mode().Perm()
	}
	journal := resolved + ".oheco-daemon.next"
	file, err := openRegular(journal, os.O_WRONLY|os.O_CREATE, modeBits)
	if err != nil {
		return err
	}
	if err = file.Chmod(modeBits); err == nil {
		err = file.Truncate(0)
	}
	if err == nil {
		_, err = file.Write(updated)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write zsh configuration journal: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	currentInfo, statErr := os.Lstat(resolved)
	current, readErr := os.ReadFile(resolved)
	if info == nil {
		if !os.IsNotExist(statErr) || !os.IsNotExist(readErr) {
			return fmt.Errorf("zsh startup target changed during edit: %s", filename)
		}
	} else if statErr != nil || readErr != nil || !os.SameFile(info, currentInfo) || !bytes.Equal(current, original) {
		return fmt.Errorf("zsh startup target changed during edit: %s", filename)
	}
	if err := os.Rename(journal, resolved); err != nil {
		return fmt.Errorf("commit zsh startup file %q: %w", filename, err)
	}
	return nil
}

func autoloadScript(root string, separator bool) string {
	var script strings.Builder
	script.WriteString(autoloadStartMarker + "\n")
	if separator {
		script.WriteString(autoloadSeparatorMarker + "\n")
	}
	script.WriteString("OHECO_ROOT=" + autoloadZshQuote(root) + " " + autoloadZshQuote(filepath.Join(root, "bin", "oo")) + " daemon </dev/null >/dev/null 2>&1 &!\n")
	script.WriteString(autoloadEndMarker + "\n")
	return script.String()
}

// ANSI-C quoting is supported by zsh and keeps even newline-containing paths
// on one physical line, where they cannot be confused with our marker lines.
func autoloadZshQuote(value string) string {
	var quoted strings.Builder
	quoted.WriteString("$'")
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch c {
		case '\\', '\'':
			quoted.WriteByte('\\')
			quoted.WriteByte(c)
		case '\n':
			quoted.WriteString("\\n")
		case '\r':
			quoted.WriteString("\\r")
		case '\t':
			quoted.WriteString("\\t")
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&quoted, "\\%03o", c)
			} else {
				quoted.WriteByte(c)
			}
		}
	}
	quoted.WriteByte('\'')
	return quoted.String()
}

func autoloadFindBlock(content []byte) (*autoloadBlock, error) {
	var block *autoloadBlock
	inside := false
	for offset := 0; offset < len(content); {
		end := bytes.IndexByte(content[offset:], '\n')
		next := len(content)
		if end < 0 {
			end = len(content)
		} else {
			end += offset
			next = end + 1
		}
		line := strings.TrimSuffix(string(content[offset:end]), "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case line == autoloadStartMarker:
			if block != nil {
				return nil, fmt.Errorf("malformed or duplicate daemon autoload marker blocks")
			}
			block = &autoloadBlock{start: offset}
			inside = true
		case line == autoloadEndMarker:
			if block == nil || !inside {
				return nil, fmt.Errorf("malformed or duplicate daemon autoload marker blocks")
			}
			inside = false
			block.end = next
			block.finalNewline = next > end
		case strings.HasPrefix(trimmed, "# >>> oheco daemon autoload"), strings.HasPrefix(trimmed, "# <<< oheco daemon autoload"):
			return nil, fmt.Errorf("malformed daemon autoload marker line")
		case inside && line == autoloadSeparatorMarker:
			if block.addedSeparator || block.start == 0 || content[block.start-1] != '\n' {
				return nil, fmt.Errorf("malformed daemon autoload separator marker")
			}
			block.addedSeparator = true
		}
		offset = next
	}
	if inside {
		return nil, fmt.Errorf("daemon autoload block is missing its closing marker")
	}
	return block, nil
}
