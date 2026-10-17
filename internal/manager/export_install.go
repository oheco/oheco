package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

// ExportOptions applies only to the explicit --install extension. Plain Export
// retains its file-only behavior and never probes tools, devices or login state.
type ExportOptions struct {
	Install                 bool
	Device, TeamID, Product string
	Yes, DryRun             bool
}

const projectInstallMarker = ".oo-export-install.json"

type exportInstallReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	Package       string `json:"package"`
	Version       string `json:"version"`
	Project       string `json:"project"`
	SHA256        string `json:"sha256"`
	Status        string `json:"status"`
	Device        string `json:"device,omitempty"`
	Bundle        string `json:"bundle,omitempty"`
	VersionCode   int64  `json:"version_code,omitempty"`
	Artifact      string `json:"artifact,omitempty"`
	InstalledAt   string `json:"installed_at,omitempty"`
}
type projectInstaller struct {
	m                                             *Manager
	directory, work, device, udid, team, identity string
	options                                       ExportOptions
	tools                                         projectTools
	metadata                                      projectMetadata
	secrets                                       []string
	run                                           projectCommand
}

func (m *Manager) ExportWithOptions(ctx context.Context, spec, projectName, destination string, options ExportOptions) error {
	if !options.Install {
		return m.Export(ctx, spec, projectName, destination)
	}
	name, version, err := SplitSpec(spec)
	if err != nil {
		return err
	}
	if destination == "" {
		destination = "."
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	var project catalog.Project
	err = m.withLock(func() error {
		idx, e := m.LoadIndex()
		if e != nil {
			return e
		}
		p, e := idx.Find(name)
		if e != nil {
			return e
		}
		version, projectName, project, e = p.ResolveProject(version, projectName, m.Platform)
		return e
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(m.Out, "Export/install plan: %s@%s project %s → %s\n", name, version, projectName, destination)
	fmt.Fprintln(m.Out, "Use existing compatible tools; if missing, install command-line-tools, Node.js >=22 and @deveco/deveco-cli@"+projectDevEcoVersion+".")
	fmt.Fprintln(m.Out, "Command Line Tools currently require about 2.03 GiB download and 6.98 GiB installed, plus staging space. Login uses browser authorization; device settings and trust require your action.")
	fmt.Fprintln(m.Out, "Build the selected DevEco product in debug mode and replace-install it with HDC, preserving existing application data. SDK/tool and project dependencies use their configured sources.")
	if options.DryRun {
		fmt.Fprintln(m.Out, "Dry run: no project download, tool installation, login, device connection, build or installation was started.")
		return nil
	}
	if err = m.confirmContext(ctx, "Continue with this export, tool preparation, debug signing and installation?", options.Yes); err != nil {
		return err
	}
	if err = os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		return err
	}
	operationLock, err := os.OpenFile(filepath.Join(destination, ".oo-export-install.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer operationLock.Close()
	if err = syscall.Flock(int(operationLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another export/install operation is using this project: %w", err)
	}
	defer syscall.Flock(int(operationLock.Fd()), syscall.LOCK_UN)
	receipt := exportInstallReceipt{SchemaVersion: 1, Package: name, Version: version, Project: projectName, SHA256: project.SHA256, Status: "exported"}
	var existing exportInstallReceipt
	marker := filepath.Join(destination, projectInstallMarker)
	if e := readJSON(marker, &existing); e == nil {
		if existing.SchemaVersion != 1 || existing.Package != name || existing.Version != version || existing.Project != projectName || existing.SHA256 != project.SHA256 {
			return fmt.Errorf("export directory belongs to a different export/install operation; choose another --output directory")
		}
		fmt.Fprintln(m.Out, "Resuming the existing exported project; its current files are retained.")
	} else {
		if !errors.Is(e, os.ErrNotExist) {
			return fmt.Errorf("cannot resume export/install: %w", e)
		}
		if err = m.Export(ctx, name+"@"+version, projectName, destination); err != nil {
			return err
		}
		if _, e = os.Lstat(marker); !errors.Is(e, os.ErrNotExist) {
			return fmt.Errorf("the project contains the reserved file %s", projectInstallMarker)
		}
		if err = writeJSON(marker, receipt); err != nil {
			return err
		}
	}
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "oo-export-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	installer := &projectInstaller{m: m, directory: destination, work: work, options: options, identity: name + "/" + projectName, run: m.projectCommand}
	// The operation was confirmed once; nested tool plans remain visible but do
	// not ask again or recursively hold the package-state lock.
	installer.options.Yes = true
	err = installer.install(ctx)
	if err != nil {
		return fmt.Errorf("exported project retained at %s; fix the reported problem and rerun the same oo export command with --install to resume: %w", destination, err)
	}
	receipt.Status = "installed"
	receipt.Device = installer.device
	receipt.Bundle = installer.metadata.bundle
	receipt.VersionCode = installer.metadata.versionCode
	receipt.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	if err = writeJSON(filepath.Join(destination, projectInstallMarker), receipt); err != nil {
		return fmt.Errorf("application installed, but installation record could not be saved: %w", err)
	}
	fmt.Fprintf(m.Out, "Installed %s version %s (%d) on %s. Exported project: %s\n", receipt.Bundle, installer.metadata.versionName, receipt.VersionCode, receipt.Device, destination)
	return nil
}
func (i *projectInstaller) install(ctx context.Context) error {
	if err := restoreProjectProfile(i.directory); err != nil {
		return err
	}
	var err error
	i.metadata, err = loadProjectMetadata(i.directory, i.options.Product)
	if err != nil {
		return err
	}
	if err = i.prepareTools(ctx); err != nil {
		return err
	}
	toolEnv, closeProxy, err := prepareProjectProxy(ctx, i.tools.env)
	if err != nil {
		return err
	}
	defer closeProxy()
	i.tools.env = toolEnv
	if err = i.selectDevice(ctx); err != nil {
		return err
	}
	// Never build/install a product whose minimum SDK exceeds the target device.
	output, err := i.command(ctx, 20*time.Second, "query device API", i.tools.hdc, "-t", i.device, "shell", "param", "get", "const.ohos.apiversion")
	if err != nil {
		return err
	}
	api := projectAPI(strings.TrimSpace(string(output)))
	minimum := projectAPI(fmt.Sprint(i.metadata.product["compatibleSdkVersion"]))
	if api == 0 || minimum == 0 || api < minimum || api < 22 {
		return fmt.Errorf("device API %d cannot install this APP product (requires API >=%d and APP installation support >=22)", api, minimum)
	}
	material, materialErr := i.metadata.material(i.directory)
	if materialErr == nil {
		materialErr = i.verifyMaterial(ctx, material)
	}
	if materialErr != nil {
		fmt.Fprintln(i.m.Out, "Preparing an automatic debug signing configuration for the selected device.")
		if err = i.prepareAutomaticSigning(ctx); err != nil {
			return err
		}
		material, err = i.metadata.material(i.directory)
		if err != nil {
			return err
		}
		if err = i.verifyMaterial(ctx, material); err != nil {
			return err
		}
	}
	i.secrets = append(i.secrets, material["keyPassword"], material["storePassword"])
	return i.buildAndInstall(ctx)
}
func projectAPI(value string) int {
	value = strings.TrimSpace(value)
	if open := strings.LastIndexByte(value, '('); open >= 0 && strings.HasSuffix(value, ")") {
		value = value[open+1 : len(value)-1]
	}
	var result int
	if strings.Contains(value, ".") {
		_, _ = fmt.Sscanf(value, "%d.", &result)
		if result < 17 {
			return 0
		}
		return result
	}
	if _, err := fmt.Sscanf(value, "%d", &result); err != nil {
		return 0
	}
	return result
}
func (i *projectInstaller) login(ctx context.Context) error {
	output, err := i.cli(ctx, 30*time.Second, "check developer login", "auth", "status")
	if err == nil && strings.Contains(string(output), "Current user:") {
		return nil
	}
	if !strings.Contains(strings.ToLower(string(output)), "not logged in") {
		if err != nil {
			return err
		}
		return fmt.Errorf("DevEco CLI could not verify its login state; run devecocli auth login and retry")
	}
	if file, ok := i.m.In.(*os.File); ok {
		info, e := file.Stat()
		if e != nil || info.Mode()&os.ModeCharDevice == 0 {
			return fmt.Errorf("developer login is required; run devecocli auth login in a terminal and retry")
		}
	}
	fmt.Fprintln(i.m.Out, "Developer login is required. DevEco CLI will print a link; open it in this machine's browser and complete authorization, then return here.")
	loginCtx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	defer cancel()
	program, args := i.cliCommand([]string{"auth", "login"})
	_, err = i.run(loginCtx, program, args, i.directory, i.tools.env, true)
	if err != nil {
		return fmt.Errorf("developer login: %w", err)
	}
	output, err = i.cli(ctx, 30*time.Second, "verify developer login", "auth", "status")
	if err != nil {
		return err
	}
	if !strings.Contains(string(output), "Current user:") {
		return fmt.Errorf("developer login was not completed")
	}
	return nil
}
func projectDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func projectWriteDocument(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}
