package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const projectDevEcoVersion = "1.3.5"

type projectTools struct {
	node, cli, clt, hdc, verifier string
	env                           []string
}

func regularProjectTool(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func canonicalProjectPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return real
	}
	return absolute
}
func projectCLTAt(root string) bool {
	for _, rel := range []string{"node/bin/node", "hvigor/bin/hvigorw.js", "ohpm/bin/pm-cli.js", "sdk/default/openharmony/toolchains/hdc", "sdk/default/openharmony/toolchains/lib/hap-sign-tool"} {
		if !regularProjectTool(filepath.Join(root, rel)) {
			return false
		}
	}
	return true
}
func (m *Manager) findProjectCLT() (string, error) {
	if selected := os.Getenv("COMMAND_LINE_TOOL_PATH"); selected != "" {
		selected = canonicalProjectPath(selected)
		if !projectCLTAt(selected) {
			return "", fmt.Errorf("COMMAND_LINE_TOOL_PATH does not select complete Command Line Tools: %s", selected)
		}
		return selected, nil
	}
	for _, name := range []string{"hdc", "hvigor"} {
		if program, err := m.findLanguageExecutable(name); err == nil {
			root := filepath.Dir(canonicalProjectPath(program))
			for n := 0; n < 7; n++ {
				if projectCLTAt(root) {
					return root, nil
				}
				root = filepath.Dir(root)
			}
		}
	}
	state, err := m.LoadState()
	if err != nil {
		return "", err
	}
	if p := state.Packages["command-line-tools"]; p.Active != "" {
		root := filepath.Join(m.Root, "packages/command-line-tools", p.Active)
		if projectCLTAt(root) {
			return canonicalProjectPath(root), nil
		}
	}
	return "", nil
}
func (i *projectInstaller) installTool(ctx context.Context, spec string) error {
	fmt.Fprintf(i.m.Out, "Required tool: %s. oo will show its installation plan.\n", spec)
	return i.m.InstallMany(ctx, []string{spec}, InstallOptions{Yes: i.options.Yes})
}
func (i *projectInstaller) prepareTools(ctx context.Context) error {
	var err error
	i.tools.clt, err = i.m.findProjectCLT()
	if err != nil {
		return err
	}
	if i.tools.clt == "" {
		if err = i.installTool(ctx, "command-line-tools"); err != nil {
			return err
		}
		i.tools.clt, err = i.m.findProjectCLT()
		if err != nil {
			return err
		}
		if i.tools.clt == "" {
			return fmt.Errorf("installed Command Line Tools could not be located")
		}
	}
	i.tools.node, _ = i.m.findLanguageExecutable("node")
	if !i.compatibleNode(ctx) {
		if err = i.installTool(ctx, "nodejs"); err != nil {
			return err
		}
		i.tools.node, err = i.m.findLanguageExecutable("node")
		if err != nil {
			return err
		}
		if !i.compatibleNode(ctx) {
			return fmt.Errorf("Node.js 22 or newer is required; the selected nodejs package is incompatible")
		}
	}
	i.tools.node = canonicalProjectPath(i.tools.node)
	i.tools.hdc = filepath.Join(i.tools.clt, "sdk/default/openharmony/toolchains/hdc")
	i.tools.verifier = filepath.Join(i.tools.clt, "sdk/default/openharmony/toolchains/lib/hap-sign-tool")
	i.tools.env = languageEnv("npm")
	i.tools.env = setLanguageEnv(i.tools.env, "COMMAND_LINE_TOOL_PATH", i.tools.clt)
	i.tools.env = setLanguageEnv(i.tools.env, "DEVECO_SDK_HOME", filepath.Join(i.tools.clt, "sdk"))
	i.tools.env = prependLanguagePath(i.tools.env, filepath.Dir(i.tools.node))
	i.tools.env = setLanguageEnv(i.tools.env, "NODE_HOME", filepath.Join(i.tools.clt, "node/bin"))
	// The bundled Node needs libc++_shared.so, while HDC must not see LLVM's
	// libc++.so linker script. Expose only the one runtime, for this operation.
	runtime := filepath.Join(i.tools.clt, "sdk/default/openharmony/native/llvm/lib/aarch64-linux-ohos/libc++_shared.so")
	if regularProjectTool(runtime) {
		dir := filepath.Join(i.work, "node-runtime")
		if err = os.Mkdir(dir, 0755); err != nil {
			return err
		}
		if err = os.Symlink(canonicalProjectPath(runtime), filepath.Join(dir, "libc++_shared.so")); err != nil {
			return err
		}
		loader := dir
		if inherited := os.Getenv("LD_LIBRARY_PATH"); inherited != "" {
			loader += ":" + inherited
		}
		i.tools.env = setLanguageEnv(i.tools.env, "LD_LIBRARY_PATH", loader)
	}
	fmt.Fprintf(i.m.Out, "Using Node.js and Command Line Tools at %s\n", i.tools.clt)
	return nil
}
func (i *projectInstaller) compatibleNode(ctx context.Context) bool {
	if i.tools.node == "" {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := i.run(probe, i.tools.node, []string{"--version"}, i.directory, languageEnv("npm"), false)
	major := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(output)), "v"), ".")[0]
	n, _ := strconv.Atoi(major)
	return err == nil && n >= 22
}
func (i *projectInstaller) findCLI(ctx context.Context) (string, error) {
	if selected := os.Getenv("OHECO_DEVECOCLI"); selected != "" {
		if regularProjectTool(selected) && (strings.HasSuffix(selected, ".js") || strings.HasSuffix(selected, ".cjs") || strings.HasSuffix(selected, ".mjs")) {
			return canonicalProjectPath(selected), nil
		}
		program, err := exec.LookPath(selected)
		if err != nil {
			return "", fmt.Errorf("OHECO_DEVECOCLI: %w", err)
		}
		return canonicalProjectPath(program), nil
	}
	candidates := []string{filepath.Join(filepath.Dir(i.tools.node), "devecocli"), filepath.Join(filepath.Dir(filepath.Dir(i.tools.node)), "lib/node_modules/@deveco/deveco-cli/cli.js")}
	if program, err := i.m.findLanguageExecutable("devecocli"); err == nil {
		candidates = append(candidates, program)
	}
	for _, candidate := range candidates {
		if regularProjectTool(candidate) {
			return canonicalProjectPath(candidate), nil
		}
	}
	// A user-configured npm prefix can place the public CLI outside Node's tree.
	npm := filepath.Join(filepath.Dir(filepath.Dir(i.tools.node)), "lib/node_modules/npm/bin/npm-cli.js")
	if regularProjectTool(npm) {
		output, err := i.command(ctx, 20*time.Second, "locate npm global packages", i.tools.node, npm, "root", "--global")
		if err == nil {
			candidate := filepath.Join(strings.TrimSpace(string(output)), "@deveco/deveco-cli/cli.js")
			if regularProjectTool(candidate) {
				return canonicalProjectPath(candidate), nil
			}
		}
	}
	return "", nil
}
func (i *projectInstaller) ensureCLI(ctx context.Context) error {
	cli, err := i.findCLI(ctx)
	if err != nil {
		return err
	}
	if cli != "" {
		i.tools.cli = cli
		output, probeErr := i.cli(ctx, 20*time.Second, "check DevEco CLI", "--version")
		if probeErr == nil && compatibleProjectCLI(output) {
			return nil
		}
		if os.Getenv("OHECO_DEVECOCLI") != "" {
			return fmt.Errorf("OHECO_DEVECOCLI must select a working DevEco CLI >=%s", projectDevEcoVersion)
		}
	}
	{
		if err = i.installTool(ctx, "npm:@deveco/deveco-cli@"+projectDevEcoVersion); err != nil {
			return err
		}
		cli, err = i.findCLI(ctx)
		if err != nil {
			return err
		}
	}
	if cli == "" {
		return errors.New("DevEco CLI could not be located after installation; set OHECO_DEVECOCLI to its public CLI entry file")
	}
	i.tools.cli = cli
	output, err := i.cli(ctx, 20*time.Second, "check DevEco CLI", "--version")
	if err != nil {
		return err
	}
	if !compatibleProjectCLI(output) {
		return fmt.Errorf("the selected DevEco CLI is incompatible; select version >=%s with OHECO_DEVECOCLI", projectDevEcoVersion)
	}
	return nil
}
func compatibleProjectCLI(output []byte) bool {
	var major, minor, patch int
	n, _ := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(string(output)), "v"), "%d.%d.%d", &major, &minor, &patch)
	return n == 3 && (major > 1 || major == 1 && (minor > 3 || minor == 3 && patch >= 5))
}
func (i *projectInstaller) cli(ctx context.Context, timeout time.Duration, stage string, args ...string) ([]byte, error) {
	program, argv := i.cliCommand(args)
	return i.command(ctx, timeout, stage, program, argv...)
}
func (i *projectInstaller) cliCommand(args []string) (string, []string) {
	for _, extension := range []string{".js", ".cjs", ".mjs"} {
		if strings.HasSuffix(i.tools.cli, extension) {
			return i.tools.node, append([]string{i.tools.cli}, args...)
		}
	}
	return i.tools.cli, args
}
