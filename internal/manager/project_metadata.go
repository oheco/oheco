package manager

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	json5 "github.com/oheco/oheco/internal/json5"
)

func readProjectFile(filename string) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, fmt.Errorf("expected a configuration file up to 8 MiB: %s", filename)
	}
	data := make([]byte, info.Size())
	n, err := f.ReadAt(data, 0)
	if err != nil && n != len(data) {
		return nil, err
	}
	return data, nil
}
func decodeProjectJSON(data []byte, v any) error { return json5.Unmarshal(data, v) }
func projectMap(v any) map[string]any            { result, _ := v.(map[string]any); return result }
func projectString(v any) string                 { s, _ := v.(string); return s }
func projectArray(v any) []any                   { a, _ := v.([]any); return a }

var projectBundleName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var projectIdentifier = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,255}$`)

type projectMetadata struct {
	profile                          map[string]any
	product                          map[string]any
	productName, bundle, versionName string
	versionCode                      int64
	permissions                      []string
	modules                          []string
}

func loadProjectMetadata(root, product string) (projectMetadata, error) {
	var result projectMetadata
	if err := projectJSONFile(filepath.Join(root, "build-profile.json5"), &result.profile); err != nil {
		return result, err
	}
	var scope struct {
		App struct {
			Bundle  string `json:"bundleName"`
			Version string `json:"versionName"`
			Code    int64  `json:"versionCode"`
		} `json:"app"`
	}
	if err := projectJSONFile(filepath.Join(root, "AppScope/app.json5"), &scope); err != nil {
		return result, err
	}
	result.bundle, result.versionName, result.versionCode = scope.App.Bundle, scope.App.Version, scope.App.Code
	if !projectBundleName.MatchString(result.bundle) || result.versionCode <= 0 {
		return result, fmt.Errorf("the exported DevEco project has an invalid bundleName or versionCode")
	}
	products := projectArray(projectMap(result.profile["app"])["products"])
	if product == "" {
		product = "default"
		if len(products) == 1 {
			product = projectString(projectMap(products[0])["name"])
		}
	}
	for _, entry := range products {
		p := projectMap(entry)
		if projectString(p["name"]) == product {
			if result.product != nil {
				return result, fmt.Errorf("duplicate product %q", product)
			}
			result.product = p
		}
	}
	if result.product == nil || !projectIdentifier.MatchString(product) {
		return result, fmt.Errorf("product %q is unavailable; select one with --product", product)
	}
	result.productName = product
	permissions := map[string]bool{}
	for _, entry := range projectArray(result.profile["modules"]) {
		module := projectMap(entry)
		name := projectString(module["name"])
		src := projectString(module["srcPath"])
		if !projectIdentifier.MatchString(name) || src == "" || filepath.IsAbs(src) {
			return result, fmt.Errorf("invalid DevEco module declaration")
		}
		moduleRoot := filepath.Join(root, src)
		rel, err := filepath.Rel(root, moduleRoot)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return result, fmt.Errorf("module %s leaves the exported project", name)
		}
		var descriptor struct {
			Module struct {
				Type        string `json:"type"`
				Permissions []struct {
					Name string `json:"name"`
				} `json:"requestPermissions"`
			} `json:"module"`
		}
		if err := projectJSONFile(filepath.Join(moduleRoot, "src/main/module.json5"), &descriptor); err != nil {
			return result, err
		}
		result.modules = append(result.modules, name)
		for _, permission := range descriptor.Module.Permissions {
			if permission.Name == "" {
				return result, fmt.Errorf("invalid requested permission in module %s", name)
			}
			permissions[permission.Name] = true
		}
	}
	if len(result.modules) == 0 {
		return result, fmt.Errorf("the exported project has no modules")
	}
	for permission := range permissions {
		result.permissions = append(result.permissions, permission)
	}
	sort.Strings(result.permissions)
	return result, nil
}
func (p projectMetadata) material(root string) (map[string]string, error) {
	selected := projectString(p.product["signingConfig"])
	if selected == "" {
		return nil, fmt.Errorf("no signing configuration is selected")
	}
	var config map[string]any
	for _, entry := range projectArray(projectMap(p.profile["app"])["signingConfigs"]) {
		candidate := projectMap(entry)
		if projectString(candidate["name"]) == selected {
			if config != nil {
				return nil, fmt.Errorf("duplicate signing configuration")
			}
			config = candidate
		}
	}
	raw := projectMap(config["material"])
	material := map[string]string{}
	for _, key := range []string{"certpath", "profile", "storeFile", "keyAlias", "signAlg"} {
		value := projectString(raw[key])
		if value == "" {
			return nil, fmt.Errorf("signing configuration is missing %s", key)
		}
		material[key] = value
	}
	for _, key := range []string{"keyPassword", "storePassword"} {
		material[key] = projectString(raw[key])
	}
	for _, key := range []string{"certpath", "profile", "storeFile"} {
		if !filepath.IsAbs(material[key]) {
			material[key] = filepath.Join(root, material[key])
		}
		if _, err := readProjectFile(material[key]); err != nil {
			return nil, fmt.Errorf("signing material %s is unavailable: %w", key, err)
		}
	}
	return material, nil
}
func (i *projectInstaller) requiredACL() ([]string, error) {
	var definitions struct {
		Permissions []struct {
			Name, AvailableLevel, AvailableType string
			ProvisionEnable                     bool
		} `json:"definePermissions"`
	}
	if err := projectJSONFile(filepath.Join(i.tools.clt, "sdk/default/openharmony/toolchains/lib/permissionDefinitions.json"), &definitions); err != nil {
		return nil, err
	}
	index := map[string]bool{}
	known := map[string]bool{}
	for _, d := range definitions.Permissions {
		known[d.Name] = true
		index[d.Name] = d.AvailableLevel == "system_basic" && d.AvailableType == "NORMAL" && d.ProvisionEnable
	}
	var required []string
	for _, name := range i.metadata.permissions {
		if !known[name] {
			return nil, fmt.Errorf("the selected SDK has no definition for permission %s", name)
		}
		if name == "ohos.permission.READ_DOCUMENT" || name == "ohos.permission.WRITE_DOCUMENT" {
			continue
		}
		if index[name] || name == "ohos.permission.FILE_ACCESS_PERSIST" || name == "ohos.permission.READ_WRITE_DOWNLOAD_DIRECTORY" || name == "ohos.permission.READ_WRITE_DOCUMENTS_DIRECTORY" {
			required = append(required, name)
		}
	}
	return required, nil
}
func checkProjectProfile(data []byte, bundle, udid string, required []string, certificate []byte, now time.Time) error {
	var profile struct {
		Type   string `json:"type"`
		Bundle struct {
			Name        string `json:"bundle-name"`
			Certificate string `json:"development-certificate"`
		} `json:"bundle-info"`
		Validity struct {
			Before int64 `json:"not-before"`
			After  int64 `json:"not-after"`
		} `json:"validity"`
		Debug struct {
			Type    string   `json:"device-id-type"`
			Devices []string `json:"device-ids"`
		} `json:"debug-info"`
		ACL struct {
			Allowed []string `json:"allowed-acls"`
		} `json:"acls"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return fmt.Errorf("the SDK returned an invalid verified Profile: %w", err)
	}
	if profile.Type != "debug" || profile.Bundle.Name != bundle {
		return fmt.Errorf("debug Profile does not match bundle %s", bundle)
	}
	if profile.Validity.Before > now.Unix() || profile.Validity.After <= now.Unix() {
		return fmt.Errorf("the debug Profile has expired or is not yet valid")
	}
	found := false
	for _, id := range profile.Debug.Devices {
		if strings.EqualFold(id, udid) {
			found = true
		}
	}
	if !strings.EqualFold(profile.Debug.Type, "udid") || !found {
		return fmt.Errorf("the debug Profile does not authorize the selected device; register the device in the developer team and regenerate its Profile")
	}
	allowed := map[string]bool{}
	for _, permission := range profile.ACL.Allowed {
		allowed[permission] = true
	}
	for _, permission := range required {
		if !allowed[permission] {
			return fmt.Errorf("the debug Profile does not authorize required permission %s", permission)
		}
	}
	embedded, _ := pem.Decode([]byte(profile.Bundle.Certificate))
	if embedded == nil {
		return fmt.Errorf("the Profile contains no developer certificate")
	}
	expected, err := x509.ParseCertificate(embedded.Bytes)
	if err != nil {
		return fmt.Errorf("invalid developer certificate in Profile")
	}
	matched := false
	if block, _ := pem.Decode(certificate); block == nil {
		if actual, err := x509.ParseCertificate(certificate); err == nil && string(actual.Raw) == string(expected.Raw) {
			matched = true
		}
	}
	for len(certificate) > 0 {
		block, rest := pem.Decode(certificate)
		if block == nil {
			break
		}
		certificate = rest
		if string(block.Bytes) == string(expected.Raw) {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("the signing certificate does not match the Profile")
	}
	if now.Before(expected.NotBefore) || !now.Before(expected.NotAfter) {
		return fmt.Errorf("the signing certificate is expired or not yet valid")
	}
	return nil
}
