package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/oheco/oheco/internal/catalog"
)

const SDKManifestSchema = 1

var sdkComponents = []string{"native", "ets", "js", "toolchains", "previewer"}
var sdkNumericVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
var sdkIntegerAPI = regexp.MustCompile(`^[1-9][0-9]*$`)

// SDKMetadata describes the original oh-uni-package.json; its file is never
// rewritten. Unknown upstream fields are allowed, but duplicate keys are not.
type SDKMetadata struct {
	Path            string `json:"path"`
	APIVersion      int    `json:"apiVersion"`
	PlatformVersion string `json:"platformVersion,omitempty"`
	FullAPIVersion  string `json:"fullApiVersion,omitempty"`
	ReleaseType     string `json:"releaseType"`
	Version         string `json:"version"`
}

type SDKComponent struct {
	Name           string      `json:"name"`
	Package        string      `json:"package"`
	PackageVersion string      `json:"package_version"`
	Source         string      `json:"source"` // relative to OHECO_ROOT
	Metadata       SDKMetadata `json:"metadata"`
	ReceiptSHA256  string      `json:"receipt_sha256"`
	MetadataSHA256 string      `json:"metadata_sha256"`
}

type SDKManifest struct {
	SchemaVersion  int            `json:"schema_version"`
	ViewID         string         `json:"view_id"`
	PackageVersion string         `json:"package_version"`
	SDKVersion     string         `json:"sdk_version"` // SDKmanager's inner root directory
	Components     []SDKComponent `json:"components"`
}

// Root is the SDK root to pass to SDKmanager/Hvigor, not the inner API directory.
type SDKView struct {
	ID       string      `json:"id"`
	Root     string      `json:"root"`
	Manifest SDKManifest `json:"manifest"`
}

func sdkHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func sdkRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("SDK boundary must be a real directory: %s", path)
	}
	return nil
}

// Reject case aliases even on a case-sensitive fixture filesystem; HOME may
// resolve those spellings to the same object without preserving the request.
func sdkExactEntry(dir, name string) error {
	if err := sdkRealDir(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			if entry.Name() != name {
				return fmt.Errorf("SDK path has a case-folding collision: %s", filepath.Join(dir, entry.Name()))
			}
			found = true
		}
	}
	if !found {
		return &os.PathError{Op: "SDK entry", Path: filepath.Join(dir, name), Err: os.ErrNotExist}
	}
	return nil
}

func sdkRegularFile(path string, optional bool) error {
	info, err := os.Lstat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SDK boundary must be a regular file: %s", path)
	}
	return nil
}

// Check before the existing manager lock/prepare can follow a replaced boundary.
// Existing Root ancestors are trusted; Root itself and all managed children are
// checked with Lstat. Permissions are not an ownership signal on hmdfs.
func (m *Manager) sdkBoundaries() error {
	for _, path := range []string{m.Root, filepath.Join(m.Root, "packages"), filepath.Join(m.Root, "state"), filepath.Join(m.Root, "sdk")} {
		if err := sdkRealDir(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if path != m.Root {
			if err := sdkExactEntry(m.Root, filepath.Base(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, name := range []string{"installed.json", "lock", "transaction.json"} {
		if err := sdkRegularFile(filepath.Join(m.Root, "state", name), true); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) sdkWithLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.sdkBoundaries(); err != nil {
		return err
	}
	return m.withLock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.sdkBoundaries(); err != nil {
			return err
		}
		return fn()
	})
}

func sdkReadFile(path string) ([]byte, error) {
	if err := sdkExactEntry(filepath.Dir(path), filepath.Base(path)); err != nil {
		return nil, err
	}
	if err := sdkRegularFile(path, false); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err == nil && len(data) > 1<<20 {
		return nil, fmt.Errorf("SDK JSON file is too large: %s", path)
	}
	return data, err
}

func sdkDecodeMetadata(data []byte) (SDKMetadata, error) {
	var meta SDKMetadata
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return meta, fmt.Errorf("SDK metadata must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return meta, err
		}
		key, ok := token.(string)
		if !ok {
			return meta, fmt.Errorf("invalid SDK metadata key")
		}
		if _, exists := fields[key]; exists {
			return meta, fmt.Errorf("duplicate SDK metadata field %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return meta, err
		}
		fields[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return meta, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return meta, fmt.Errorf("trailing SDK metadata content")
	}
	for key, target := range map[string]*string{"path": &meta.Path, "platformVersion": &meta.PlatformVersion, "releaseType": &meta.ReleaseType, "version": &meta.Version} {
		if value, ok := fields[key]; ok {
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, target) != nil {
				return meta, fmt.Errorf("SDK metadata %s must be a string", key)
			}
		}
	}
	api, ok := fields["apiVersion"]
	if !ok {
		return meta, fmt.Errorf("SDK metadata apiVersion must be a positive integer or decimal string")
	}
	if err := json.Unmarshal(api, &meta.APIVersion); err != nil {
		var text string
		if json.Unmarshal(api, &text) != nil || !sdkIntegerAPI.MatchString(text) {
			return meta, fmt.Errorf("SDK metadata apiVersion must be a positive integer or decimal string")
		}
		meta.APIVersion, err = strconv.Atoi(text)
		if err != nil {
			return meta, fmt.Errorf("SDK metadata apiVersion is out of range")
		}
	}
	if meta.APIVersion <= 0 {
		return meta, fmt.Errorf("SDK metadata apiVersion must be positive")
	}
	if value, ok := fields["fullApiVersion"]; ok {
		if bytes.Equal(value, []byte("null")) {
			return meta, fmt.Errorf("SDK metadata fullApiVersion must be a version string or positive integer")
		}
		if json.Unmarshal(value, &meta.FullAPIVersion) != nil {
			var n int
			if json.Unmarshal(value, &n) != nil || n <= 0 {
				return meta, fmt.Errorf("SDK metadata fullApiVersion must be a version string or positive integer")
			}
			meta.FullAPIVersion = strconv.Itoa(n)
		}
	}
	return meta, nil
}

// Stable and Release are aliases for the unsuffixed official view ID. Their
// manifests remain distinct, so any alias/content collision is fail-closed.
func sdkIdentity(meta SDKMetadata) (id, inner string, err error) {
	if !sdkNumericVersion.MatchString(meta.Version) || !catalog.ValidComponent(meta.Version) {
		return "", "", fmt.Errorf("invalid SDK metadata version %q", meta.Version)
	}
	id = meta.Version
	switch meta.ReleaseType {
	case "Beta":
		id += ".Beta"
	case "Stable", "Release":
	default:
		return "", "", fmt.Errorf("unsupported SDK releaseType %q (expected Beta, Stable or Release)", meta.ReleaseType)
	}
	if meta.APIVersion <= 0 {
		return "", "", fmt.Errorf("invalid SDK apiVersion")
	}
	if meta.PlatformVersion != "" && (!sdkNumericVersion.MatchString(meta.PlatformVersion) || !catalog.ValidComponent(meta.PlatformVersion)) {
		return "", "", fmt.Errorf("invalid SDK platformVersion %q", meta.PlatformVersion)
	}
	if meta.FullAPIVersion != "" && (!sdkNumericVersion.MatchString(meta.FullAPIVersion) || !catalog.ValidComponent(meta.FullAPIVersion)) {
		return "", "", fmt.Errorf("invalid SDK fullApiVersion %q", meta.FullAPIVersion)
	}
	if meta.APIVersion >= 26 {
		if meta.PlatformVersion == "" {
			return "", "", fmt.Errorf("SDK API >= 26 requires a nonempty platformVersion")
		}
		inner = meta.PlatformVersion
	} else if meta.FullAPIVersion != "" {
		inner = meta.FullAPIVersion
	} else {
		inner = strconv.Itoa(meta.APIVersion)
	}
	return id, inner, nil
}

func sdkValidID(id string) bool {
	return sdkNumericVersion.MatchString(strings.TrimSuffix(id, ".Beta")) && catalog.ValidComponent(id)
}

func (m *Manager) sdkSelectedManifest(version string) (SDKManifest, error) {
	manifest := SDKManifest{SchemaVersion: SDKManifestSchema, PackageVersion: version}
	if !catalog.ValidComponent(version) {
		return manifest, fmt.Errorf("invalid SDK package version %q; specify the exact installed version", version)
	}
	state, err := m.LoadState()
	if err != nil {
		return manifest, err
	}
	var missing []string
	for _, name := range sdkComponents {
		pkg := "ohos-sdk-" + name
		if _, ok := state.Packages[pkg].Versions[version]; !ok {
			missing = append(missing, pkg+"@"+version)
			continue
		}
		for _, path := range []string{filepath.Join(m.Root, "packages", pkg), filepath.Join(m.Root, "packages", pkg, version)} {
			if err := sdkExactEntry(filepath.Dir(path), filepath.Base(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return manifest, err
			}
			if err := sdkRealDir(path); errors.Is(err, os.ErrNotExist) {
				missing = append(missing, pkg+"@"+version+" (installed directory missing)")
				break
			} else if err != nil {
				return manifest, err
			}
		}
	}
	if len(missing) != 0 {
		return manifest, fmt.Errorf("SDK requires all five installed components; missing: %s; install these exact versions first (SDK create is offline)", strings.Join(missing, ", "))
	}
	var common SDKMetadata
	for i, name := range sdkComponents {
		pkg := "ohos-sdk-" + name
		receipt := state.Packages[pkg].Versions[version]
		if receipt.Platform != m.Platform {
			return manifest, fmt.Errorf("%s@%s is installed for %s, not %s", pkg, version, receipt.Platform, m.Platform)
		}
		source := filepath.ToSlash(filepath.Join("packages", pkg, version))
		data, err := sdkReadFile(filepath.Join(m.Root, filepath.FromSlash(source), "oh-uni-package.json"))
		if err != nil {
			return manifest, fmt.Errorf("%s@%s original metadata: %w", pkg, version, err)
		}
		meta, err := sdkDecodeMetadata(data)
		if err != nil {
			return manifest, fmt.Errorf("%s@%s metadata: %w", pkg, version, err)
		}
		if meta.Path != name {
			return manifest, fmt.Errorf("%s metadata path %q is not the whitelisted component %q", pkg, meta.Path, name)
		}
		id, inner, err := sdkIdentity(meta)
		if err != nil {
			return manifest, fmt.Errorf("%s metadata: %w", pkg, err)
		}
		if i == 0 {
			manifest.ViewID, manifest.SDKVersion, common = id, inner, meta
		} else {
			metaCommon := meta
			metaCommon.Path = common.Path
			if metaCommon != common {
				return manifest, fmt.Errorf("mixed SDK metadata: %s does not match native apiVersion/platformVersion/fullApiVersion/releaseType/version", pkg)
			}
		}
		// Bind only immutable package/content identity. Ownership promotion by
		// switch and install timestamps are state, not different SDK contents.
		identity := struct {
			Platform        string           `json:"platform"`
			Artifact        catalog.Artifact `json:"artifact"`
			UpstreamVersion string           `json:"upstream_version,omitempty"`
		}{receipt.Platform, receipt.Artifact, receipt.UpstreamVersion}
		rawReceipt, err := json.Marshal(identity)
		if err != nil {
			return manifest, err
		}
		manifest.Components = append(manifest.Components, SDKComponent{Name: name, Package: pkg, PackageVersion: version, Source: source, Metadata: meta, ReceiptSHA256: sdkHash(rawReceipt), MetadataSHA256: sdkHash(data)})
	}
	return manifest, nil
}

func (m SDKManifest) validate(id string) error {
	if m.SchemaVersion != SDKManifestSchema || m.ViewID != id || !sdkValidID(id) || !catalog.ValidComponent(m.PackageVersion) || len(m.Components) != len(sdkComponents) {
		return fmt.Errorf("unsupported or invalid SDK view manifest %q", id)
	}
	var common SDKMetadata
	for i, component := range m.Components {
		name := sdkComponents[i]
		if component.Name != name || component.Package != "ohos-sdk-"+name || component.PackageVersion != m.PackageVersion || component.Source != filepath.ToSlash(filepath.Join("packages", component.Package, m.PackageVersion)) || component.Metadata.Path != name {
			return fmt.Errorf("invalid SDK component source %q", component.Name)
		}
		for _, hash := range []string{component.ReceiptSHA256, component.MetadataSHA256} {
			decoded, err := hex.DecodeString(hash)
			if err != nil || len(decoded) != sha256.Size || hash != strings.ToLower(hash) {
				return fmt.Errorf("invalid SDK component digest %q", component.Name)
			}
		}
		viewID, inner, err := sdkIdentity(component.Metadata)
		if err != nil || viewID != id || inner != m.SDKVersion {
			return fmt.Errorf("invalid SDK component identity %q", component.Name)
		}
		meta := component.Metadata
		if i == 0 {
			common = meta
		} else {
			meta.Path = common.Path
			if meta != common {
				return fmt.Errorf("mixed SDK view manifest metadata")
			}
		}
	}
	return nil
}

func (m *Manager) sdkView(id string, manifest SDKManifest) SDKView {
	return SDKView{ID: id, Root: filepath.Join(m.Root, "sdk", id, "root"), Manifest: manifest}
}

func (m *Manager) sdkLinkTarget(manifest SDKManifest, component SDKComponent) (string, error) {
	return filepath.Rel(filepath.Join(m.Root, "sdk", manifest.ViewID, "root", manifest.SDKVersion), filepath.Join(m.Root, filepath.FromSlash(component.Source)))
}

// A partial view is owned only if its complete, valid manifest exists and every
// present entry matches the exact whitelist. Never follow its component links.
func (m *Manager) sdkCheckTree(manifest SDKManifest, partial bool) error {
	base := filepath.Join(m.Root, "sdk", manifest.ViewID)
	if err := sdkRealDir(base); err != nil {
		return err
	}
	levels := []struct {
		path  string
		names []string
	}{
		{base, []string{"view.json", "root"}},
		{filepath.Join(base, "root"), []string{manifest.SDKVersion}},
		{filepath.Join(base, "root", manifest.SDKVersion), sdkComponents},
	}
	for i, level := range levels {
		if err := sdkRealDir(level.path); partial && errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		entries, err := os.ReadDir(level.path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			found := false
			for _, name := range level.names {
				if entry.Name() == name {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("unmanaged SDK view content: %s", filepath.Join(level.path, entry.Name()))
			}
		}
		for j, name := range level.names {
			path := filepath.Join(level.path, name)
			info, err := os.Lstat(path)
			if partial && errors.Is(err, os.ErrNotExist) && name != "view.json" {
				continue
			}
			if err != nil {
				return fmt.Errorf("incomplete SDK view (retry sdk create %s or sdk remove %s): %w", manifest.PackageVersion, manifest.ViewID, err)
			}
			if i == 2 {
				expected, err := m.sdkLinkTarget(manifest, manifest.Components[j])
				if err != nil {
					return err
				}
				actual, err := os.Readlink(path)
				if info.Mode()&os.ModeSymlink == 0 || err != nil || actual != expected || filepath.IsAbs(actual) {
					return fmt.Errorf("unmanaged or modified SDK component link: %s", path)
				}
			} else if name == "view.json" {
				if !info.Mode().IsRegular() {
					return fmt.Errorf("SDK manifest is not a regular file: %s", path)
				}
			} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("SDK view directory was replaced: %s", path)
			}
		}
	}
	return nil
}

func (m *Manager) sdkLoadManifest(id string) (SDKManifest, error) {
	var manifest SDKManifest
	if !sdkValidID(id) {
		return manifest, fmt.Errorf("invalid SDK view ID %q", id)
	}
	if err := sdkExactEntry(filepath.Join(m.Root, "sdk"), id); err != nil {
		return manifest, err
	}
	if err := sdkRealDir(filepath.Join(m.Root, "sdk", id)); err != nil {
		return manifest, err
	}
	data, err := sdkReadFile(filepath.Join(m.Root, "sdk", id, "view.json"))
	if err != nil {
		return manifest, fmt.Errorf("unmanaged SDK view %s: %w", id, err)
	}
	if err := catalog.Decode(bytes.NewReader(data), &manifest); err != nil {
		return manifest, err
	}
	return manifest, manifest.validate(id)
}

func (m *Manager) sdkViewsLocked(partial bool) ([]SDKView, error) {
	if err := m.sdkBoundaries(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(m.Root, "sdk"))
	if errors.Is(err, os.ErrNotExist) {
		return []SDKView{}, nil
	}
	if err != nil {
		return nil, err
	}
	views := make([]SDKView, 0, len(entries))
	for _, entry := range entries {
		manifest, err := m.sdkLoadManifest(entry.Name())
		if err != nil {
			return nil, err
		}
		if err := m.sdkCheckTree(manifest, partial); err != nil {
			return nil, err
		}
		views = append(views, m.sdkView(entry.Name(), manifest))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views, nil
}

// SDKCreate is offline: it selects exactly version in LoadState, never active,
// latest, or a directory scan. Only directories, a manifest, and relative links
// are created; SDK files, executables and signatures are not changed.
func (m *Manager) SDKCreate(ctx context.Context, version string) (view SDKView, result error) {
	result = m.sdkWithLock(ctx, func() error {
		manifest, err := m.sdkSelectedManifest(version)
		if err != nil {
			return err
		}
		if err := manifest.validate(manifest.ViewID); err != nil {
			return err
		}
		sdkDir := filepath.Join(m.Root, "sdk")
		if err := plainDir(sdkDir); err != nil {
			return err
		}
		entries, err := os.ReadDir(sdkDir)
		if err != nil {
			return err
		}
		base := filepath.Join(sdkDir, manifest.ViewID)
		existing := false
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), manifest.ViewID) {
				if entry.Name() != manifest.ViewID {
					return fmt.Errorf("SDK view ID has a case-folding collision: %s", entry.Name())
				}
				old, err := m.sdkLoadManifest(manifest.ViewID)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(old, manifest) {
					return fmt.Errorf("SDK view %s already exists with a different manifest; remove the old view first", manifest.ViewID)
				}
				if err := m.sdkCheckTree(old, true); err != nil {
					return err
				}
				existing = true
			}
		}
		// The final view is a persistent deliverable, not a temporary staging
		// directory. Track identities of exclusively created nodes for rollback.
		type ownedNode struct {
			path string
			info os.FileInfo
		}
		var created []ownedNode
		record := func(path string) error {
			info, err := os.Lstat(path)
			if err == nil {
				created = append(created, ownedNode{path, info})
			}
			return err
		}
		apply := func() error {
			if !existing {
				if err := os.Mkdir(base, 0755); err != nil {
					return err
				}
				if err := record(base); err != nil {
					return err
				}
				data, err := json.MarshalIndent(manifest, "", "  ")
				if err != nil {
					return err
				}
				filename := filepath.Join(base, "view.json")
				f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0644)
				if err != nil {
					return err
				}
				if err := record(filename); err != nil {
					f.Close()
					return err
				}
				_, writeErr := f.Write(append(data, '\n'))
				if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
					return err
				}
			}
			for _, path := range []string{filepath.Join(base, "root"), filepath.Join(base, "root", manifest.SDKVersion)} {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := m.sdkBoundaries(); err != nil {
					return err
				}
				if err := m.sdkCheckTree(manifest, true); err != nil {
					return err
				}
				if err := sdkRealDir(filepath.Dir(path)); err != nil {
					return err
				}
				if err := os.Mkdir(path, 0755); errors.Is(err, os.ErrExist) && existing {
					if err := sdkRealDir(path); err != nil {
						return err
					}
				} else if err != nil {
					return err
				} else if err := record(path); err != nil {
					return err
				}
			}
			for _, component := range manifest.Components {
				if err := ctx.Err(); err != nil {
					return err
				}
				path := filepath.Join(base, "root", manifest.SDKVersion, component.Name)
				if err := m.sdkBoundaries(); err != nil {
					return err
				}
				if err := m.sdkCheckTree(manifest, true); err != nil {
					return err
				}
				if err := sdkRealDir(filepath.Dir(path)); err != nil {
					return err
				}
				target, err := m.sdkLinkTarget(manifest, component)
				if err != nil {
					return err
				}
				if err := os.Symlink(target, path); errors.Is(err, os.ErrExist) && existing {
					actual, readErr := os.Readlink(path)
					if readErr != nil || actual != target {
						return fmt.Errorf("SDK component link appeared with different contents: %s", path)
					}
				} else if err != nil {
					return err
				} else if err := record(path); err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return m.sdkCheckTree(manifest, false)
		}
		if err := apply(); err != nil {
			for i := len(created) - 1; i >= 0; i-- {
				node := created[i]
				// No RemoveAll: replaced nodes/parents and new user entries survive.
				if boundaryErr := m.sdkBoundaries(); boundaryErr != nil {
					return errors.Join(err, fmt.Errorf("SDK rollback retained artifacts: %w", boundaryErr))
				}
				parent := filepath.Dir(node.path)
				for parent != sdkDir && parent != filepath.Dir(parent) {
					if boundaryErr := sdkRealDir(parent); boundaryErr != nil {
						return errors.Join(err, fmt.Errorf("SDK rollback retained artifacts: %w", boundaryErr))
					}
					parent = filepath.Dir(parent)
				}
				info, statErr := os.Lstat(node.path)
				if errors.Is(statErr, os.ErrNotExist) {
					continue
				}
				if statErr != nil || !os.SameFile(node.info, info) {
					return errors.Join(err, fmt.Errorf("SDK rollback retained replaced artifact: %s", node.path))
				}
				if removeErr := os.Remove(node.path); removeErr != nil {
					return errors.Join(err, fmt.Errorf("SDK rollback retained %s: %w", node.path, removeErr))
				}
			}
			return err
		}
		view = m.sdkView(manifest.ViewID, manifest)
		return nil
	})
	return view, result
}

func (m *Manager) SDKList() (views []SDKView, result error) {
	result = m.sdkWithLock(context.Background(), func() error {
		var err error
		views, err = m.sdkViewsLocked(false)
		return err
	})
	return views, result
}

func (m *Manager) SDKPath(id string) (root string, result error) {
	result = m.sdkWithLock(context.Background(), func() error {
		manifest, err := m.sdkLoadManifest(id)
		if err != nil {
			return err
		}
		if err := m.sdkCheckTree(manifest, false); err != nil {
			return err
		}
		selected, err := m.sdkSelectedManifest(manifest.PackageVersion)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(selected, manifest) {
			return fmt.Errorf("SDK view %s source receipts/metadata changed; refusing to return a modified SDK", id)
		}
		root = m.sdkView(id, manifest).Root
		return nil
	})
	return root, result
}

// SDKRemove unlinks only whitelisted view entries, never source packages. The
// manifest is removed last, allowing an interrupted removal to be retried.
func (m *Manager) SDKRemove(ctx context.Context, id string) error {
	if !sdkValidID(id) {
		return fmt.Errorf("invalid SDK view ID %q", id)
	}
	return m.sdkWithLock(ctx, func() error {
		manifest, err := m.sdkLoadManifest(id)
		if errors.Is(err, os.ErrNotExist) {
			// A missing view is idempotent, but an existing manifest-less
			// directory is unmanaged and must not be removed.
			if _, statErr := os.Lstat(filepath.Join(m.Root, "sdk", id)); errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
		}
		if err != nil {
			return err
		}
		if err := m.sdkCheckTree(manifest, true); err != nil {
			return err
		}
		base := filepath.Join(m.Root, "sdk", id)
		paths := make([]string, 0, len(sdkComponents)+4)
		for _, name := range sdkComponents {
			paths = append(paths, filepath.Join(base, "root", manifest.SDKVersion, name))
		}
		paths = append(paths, filepath.Join(base, "root", manifest.SDKVersion), filepath.Join(base, "root"))
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Recheck all present boundaries/links on each step; do not follow
			// a replaced directory even if it appeared after the preflight.
			if err := m.sdkBoundaries(); err != nil {
				return err
			}
			if err := m.sdkCheckTree(manifest, true); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := m.sdkCheckTree(manifest, true); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(base, "view.json")); err != nil {
			return err
		}
		return os.Remove(base) // only succeeds if still empty
	})
}

// SDKRemovalGuard must run INSIDE the existing native manager lock, immediately
// before committing the final before/after plan (including autoremove/cascade).
// It intentionally does not acquire a second lock. Malformed/unmanaged SDK
// content fails closed. Valid partial views protect references as well.
func (m *Manager) SDKRemovalGuard(before, after State) error {
	var removals []string
	for name, installed := range before.Packages {
		for version := range installed.Versions {
			if _, keep := after.Packages[name].Versions[version]; !keep {
				removals = append(removals, name+"@"+version)
			}
		}
	}
	if len(removals) == 0 {
		return nil
	}
	views, err := m.sdkViewsLocked(true)
	if err != nil {
		return fmt.Errorf("cannot verify SDK references; refusing native removal: %w", err)
	}
	removed := map[string]bool{}
	for _, key := range removals {
		removed[key] = true
	}
	var blocked []string
	for _, view := range views {
		for _, component := range view.Manifest.Components {
			key := component.Package + "@" + component.PackageVersion
			if removed[key] {
				blocked = append(blocked, key+" (SDK view "+view.ID+")")
			}
		}
	}
	if len(blocked) != 0 {
		sort.Strings(blocked)
		return fmt.Errorf("packages are referenced by SDK views; run oo sdk remove <view-id> first: %s", strings.Join(blocked, ", "))
	}
	return nil
}
