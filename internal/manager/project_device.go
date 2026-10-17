package manager

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) projectAnswer(ctx context.Context, prompt string) (string, error) {
	if m.In == nil {
		return "", fmt.Errorf("%s: interactive input is unavailable; specify --device, --team or --product", prompt)
	}
	fmt.Fprint(m.Out, prompt)
	if file, ok := m.In.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeCharDevice == 0 {
			return "", fmt.Errorf("interactive input is unavailable; specify --device, --team or --product")
		}
		return terminalConfirmation(ctx, file)
	}
	reader, ok := m.In.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(m.In)
		m.In = reader
	}
	answer, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("input cancelled: %w", err)
	}
	if len(answer) > 4096 {
		return "", fmt.Errorf("input is too long")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}
func projectTargets(output []byte) []string {
	var targets []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 1 && !strings.ContainsAny(line, "\r\x00") {
			targets = append(targets, line)
		}
	}
	return targets
}
func projectTCPAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.Atoi(port)
	return err == nil && e == nil && host != "" && n > 0 && n <= 65535
}

var projectUDID = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

func (i *projectInstaller) selectDevice(ctx context.Context) error {
	selected := i.options.Device
	for {
		output, err := i.command(ctx, 20*time.Second, "list HDC devices", i.tools.hdc, "list", "targets")
		if err != nil {
			return err
		}
		targets := projectTargets(output)
		if selected == "" && len(targets) == 1 {
			selected = targets[0]
		}
		if selected != "" {
			present := false
			for _, target := range targets {
				if target == selected {
					present = true
				}
			}
			if !present && projectTCPAddress(selected) {
				fmt.Fprintf(i.m.Out, "Connecting HDC to %s; unlock the device and accept its trust prompt.\n", selected)
				_, err = i.command(ctx, 30*time.Second, "connect remote debugging device", i.tools.hdc, "tconn", selected)
				if err != nil {
					return err
				}
			}
			i.device = selected
			output, err = i.command(ctx, 20*time.Second, "read selected device identity", i.tools.hdc, "-t", selected, "shell", "bm", "get", "-u")
			if err != nil {
				return err
			}
			ids := projectUDID.FindAllString(string(output), -1)
			if len(ids) != 1 {
				return fmt.Errorf("cannot read the selected device UDID; unlock it, enable USB/remote debugging and accept the HDC trust prompt, then retry with --device %q", selected)
			}
			i.udid = strings.ToLower(ids[0])
			return nil
		}
		if len(targets) > 1 {
			for n, target := range targets {
				fmt.Fprintf(i.m.Out, "  %d. %s\n", n+1, target)
			}
		}
		fmt.Fprintln(i.m.Out, "Enable remote debugging in device settings and enter its IP:port, or connect USB and accept the device trust prompt.")
		answer, err := i.m.projectAnswer(ctx, "Device number or IP:port (Enter to rescan, q to cancel): ")
		if err != nil {
			return err
		}
		if answer == "q" {
			return fmt.Errorf("device selection cancelled")
		}
		if answer == "" {
			continue
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(targets) {
			selected = targets[n-1]
		} else if projectTCPAddress(answer) {
			selected = answer
		} else {
			return fmt.Errorf("invalid device selection; use a displayed number or IP:port")
		}
	}
}
func parseProjectTeams(output []byte) []string {
	var teams []string
	table := false
	for _, line := range strings.Split(string(output), "\n") {
		line = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`).ReplaceAllString(line, "")
		if strings.Contains(line, "└") || strings.Contains(line, "╰") {
			table = false
			continue
		}
		line = strings.NewReplacer("│", " ", "┃", " ", "|", " ").Replace(line)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.EqualFold(fields[0], "Id") && strings.EqualFold(fields[1], "Name") {
			table = true
			continue
		}
		if table && projectIdentifier.MatchString(fields[0]) {
			teams = append(teams, fields[0])
		}
	}
	return teams
}
func (i *projectInstaller) selectTeam(ctx context.Context) error {
	if i.options.TeamID != "" {
		if !projectIdentifier.MatchString(i.options.TeamID) {
			return fmt.Errorf("invalid signing team ID")
		}
		i.team = i.options.TeamID
		return nil
	}
	output, err := i.cli(ctx, 30*time.Second, "list signing teams", "auth", "team", "list")
	if err != nil {
		return err
	}
	teams := parseProjectTeams(output)
	if len(teams) == 1 {
		i.team = teams[0]
		return nil
	}
	if len(teams) == 0 {
		return fmt.Errorf("no developer signing team is available; complete developer account verification and specify --team")
	}
	fmt.Fprintln(i.m.Out, string(output))
	answer, err := i.m.projectAnswer(ctx, "Signing team ID: ")
	if err != nil {
		return err
	}
	for _, team := range teams {
		if answer == team {
			i.team = team
			return nil
		}
	}
	return fmt.Errorf("select a listed developer team with --team")
}
