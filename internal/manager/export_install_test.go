package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func projectTestFile(t *testing.T, root, relative string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func projectTestHAP(t *testing.T, root, relative, bundle string, version int64) {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	file, err := archive.Create("module.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(file).Encode(map[string]any{"app": map[string]any{"bundleName": bundle, "versionCode": version}, "module": map[string]any{"name": "entry", "type": "entry"}}); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	projectTestFile(t, root, relative, data.Bytes())
}
func projectTestMetadata(t *testing.T, root string) projectMetadata {
	t.Helper()
	projectTestFile(t, root, "build-profile.json5", []byte(`// DevEco profile
{app:{products:[{name:'default',runtimeOS:'OpenHarmony',compatibleSdkVersion:22}]},modules:[{name:'entry',srcPath:'./entry'}]}`))
	projectTestFile(t, root, "AppScope/app.json5", []byte(`{app:{bundleName:'org.oheco.exportfixture',versionName:'1.0测试',versionCode:12}}`))
	projectTestFile(t, root, "entry/src/main/module.json5", []byte(`{module:{type:'entry',requestPermissions:[{name:'ohos.permission.INTERNET'}]}}`))
	metadata, err := loadProjectMetadata(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}
func projectTestCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func projectTestProfile(t *testing.T, certificate []byte) []byte {
	t.Helper()
	profile := map[string]any{"type": "debug", "bundle-info": map[string]any{"bundle-name": "org.oheco.exportfixture", "development-certificate": string(certificate)}, "validity": map[string]any{"not-before": time.Now().Add(-time.Hour).Unix(), "not-after": time.Now().Add(time.Hour).Unix()}, "debug-info": map[string]any{"device-id-type": "udid", "device-ids": []string{strings.Repeat("a", 64)}}, "acls": map[string]any{"allowed-acls": []string{"permission.fixture"}}}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestExportInstallDryRunDoesNotDownloadOrExport(t *testing.T) {
	f := exportFixture(t, archive(t, entry{name: "app/source", content: "payload"}), "tar.gz")
	f.server.Close()
	var out bytes.Buffer
	f.m.Out = &out
	destination := filepath.Join(t.TempDir(), "absent")
	if err := f.m.ExportWithOptions(context.Background(), "app", "editor", destination, ExportOptions{Install: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote exported project", err)
	}
	if !strings.Contains(out.String(), "Dry run") || !strings.Contains(out.String(), projectDevEcoVersion) {
		t.Fatal(out.String())
	}
}
func TestExportInstallCannotResumeDifferentArtifact(t *testing.T) {
	f := exportFixture(t, archive(t, entry{name: "app/source", content: "payload"}), "tar.gz")
	root := t.TempDir()
	marker := exportInstallReceipt{SchemaVersion: 1, Package: "app", Version: "1.0", Project: "editor", SHA256: strings.Repeat("0", 64)}
	if err := writeJSON(filepath.Join(root, projectInstallMarker), marker); err != nil {
		t.Fatal(err)
	}
	err := f.m.ExportWithOptions(context.Background(), "app", "editor", root, ExportOptions{Install: true, Yes: true})
	if err == nil || !strings.Contains(err.Error(), "different export/install") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "source")); !os.IsNotExist(err) {
		t.Fatal("overwrote another export")
	}
}
func TestProjectMetadataProductAndModuleBoundaries(t *testing.T) {
	root := t.TempDir()
	metadata := projectTestMetadata(t, root)
	if metadata.productName != "default" || metadata.versionCode != 12 || metadata.versionName != "1.0测试" || len(metadata.permissions) != 1 {
		t.Fatalf("%+v", metadata)
	}
	if _, err := loadProjectMetadata(root, "missing"); err == nil {
		t.Fatal("accepted missing product")
	}
	projectTestFile(t, root, "build-profile.json5", []byte(`{app:{products:[{name:'default'}]},modules:[{name:'entry',srcPath:'../outside'}]}`))
	if _, err := loadProjectMetadata(root, ""); err == nil {
		t.Fatal("accepted escaping module")
	}
}
func TestProjectProfileRejectsOtherDevicePermissionAndCertificate(t *testing.T) {
	certificate := projectTestCertificate(t)
	data := projectTestProfile(t, certificate)
	now := time.Now()
	for _, tc := range []struct {
		name, bundle, udid string
		permissions        []string
		cert               []byte
		when               time.Time
		want               bool
	}{
		{"valid", "org.oheco.exportfixture", strings.Repeat("A", 64), []string{"permission.fixture"}, certificate, now, true},
		{"wrong bundle", "org.other.fixture", strings.Repeat("a", 64), nil, certificate, now, false},
		{"wrong device", "org.oheco.exportfixture", strings.Repeat("b", 64), nil, certificate, now, false},
		{"missing ACL", "org.oheco.exportfixture", strings.Repeat("a", 64), []string{"permission.other"}, certificate, now, false},
		{"wrong certificate", "org.oheco.exportfixture", strings.Repeat("a", 64), nil, projectTestCertificate(t), now, false},
		{"expired", "org.oheco.exportfixture", strings.Repeat("a", 64), nil, certificate, now.Add(2 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkProjectProfile(data, tc.bundle, tc.udid, tc.permissions, tc.cert, tc.when)
			if (err == nil) != tc.want {
				t.Fatal(err)
			}
		})
	}
}
func TestProjectDeviceConnectionUsesSelectedTarget(t *testing.T) {
	m, _ := New(t.TempDir(), "", &bytes.Buffer{})
	i := &projectInstaller{m: m, directory: t.TempDir(), options: ExportOptions{Device: "192.168.1.2:45678"}, tools: projectTools{hdc: "hdc"}}
	var calls [][]string
	i.run = func(_ context.Context, _ string, args []string, _ string, _ []string, _ bool) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		switch strings.Join(args, " ") {
		case "list targets":
			return []byte("[Empty]\n"), nil
		case "tconn 192.168.1.2:45678":
			return []byte("Connect OK"), nil
		case "-t 192.168.1.2:45678 shell bm get -u":
			return []byte(strings.Repeat("a", 64)), nil
		}
		return nil, errors.New("unexpected command")
	}
	if err := i.selectDevice(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i.udid != strings.Repeat("a", 64) || len(calls) != 3 {
		t.Fatal(i.udid, calls)
	}
}
func TestProjectTeamTableAndInstalledVersionParsing(t *testing.T) {
	teams := parseProjectTeams([]byte("┌────┬────┐\n│ Id │ Name │\n│ team-a │ Test │\n└────┴────┘\n"))
	if len(teams) != 1 || teams[0] != "team-a" {
		t.Fatal(teams)
	}
	output := []byte("org.oheco.exportfixture:\n{\"name\":\"org.oheco.exportfixture\",\"versionCode\":12}")
	if !projectInstalledVersion(output, "org.oheco.exportfixture", 12) || projectInstalledVersion(output, "org.oheco.exportfixture", 11) || projectInstalledVersion(output, "org.other", 12) {
		t.Fatal("wrong installed version result")
	}
}
func TestProjectBuildRejectsStaleAndFalseHdcSuccessAndRestoresProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []string{"success", "stale", "missing-output", "unsigned", "wrong-profile", "hdc-failure", "wrong-installed-version", "build-failure", "wrong-app-version", "changed-device"} {
		t.Run(mode, func(t *testing.T) {
			root, work := t.TempDir(), t.TempDir()
			metadata := projectTestMetadata(t, root)
			original, _ := os.ReadFile(filepath.Join(root, "build-profile.json5"))
			clt := t.TempDir()
			projectTestFile(t, clt, projectSDKCacheRelative, []byte(`module.exports = {};`))
			projectTestFile(t, clt, "sdk/default/openharmony/toolchains/lib/permissionDefinitions.json", []byte(`{"definePermissions":[{"Name":"ohos.permission.INTERNET"}]}`))
			certificate := projectTestCertificate(t)
			profile := projectTestProfile(t, certificate)
			if mode == "wrong-profile" {
				profile = bytes.ReplaceAll(profile, []byte("org.oheco.exportfixture"), []byte("org.oheco.other"))
			}
			artifact := filepath.Join("entry/build/default/outputs/default", "entry-default-signed.hap")
			if mode == "stale" {
				projectTestFile(t, root, artifact, []byte("old-app"))
			}
			m, _ := New(t.TempDir(), "", &bytes.Buffer{})
			i := &projectInstaller{m: m, directory: root, work: work, device: "device", udid: strings.Repeat("a", 64), metadata: metadata, tools: projectTools{clt: clt, hdc: "hdc", verifier: "signer"}}
			installs := 0
			i.run = func(_ context.Context, program string, args []string, _ string, _ []string, _ bool) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "assembleApp") {
					if mode == "build-failure" {
						return []byte("build failed"), errors.New("failure")
					}
					if mode != "stale" && mode != "missing-output" {
						code := int64(12)
						if mode == "wrong-app-version" {
							code = 11
						}
						projectTestHAP(t, root, artifact, "org.oheco.exportfixture", code)
					}
					if mode == "missing-output" {
						projectTestFile(t, root, "entry/build/default/outputs/default/other-signed.hap", []byte("wrong-output"))
					}
				}
				if program == "signer" && args[0] == "verify-app" {
					if mode == "unsigned" {
						return []byte("no signature file"), errors.New("invalid signature")
					}
					for n, arg := range args {
						if arg == "-outProfile" {
							projectTestFile(t, "", args[n+1], []byte("profile"))
						}
						if arg == "-outCertChain" {
							projectTestFile(t, "", args[n+1], certificate)
						}
					}
				}
				if program == "signer" && args[0] == "verify-profile" {
					for n, arg := range args {
						if arg == "-outFile" {
							projectTestFile(t, "", args[n+1], profile)
						}
					}
				}
				if program == "hdc" && len(args) > 2 && args[2] == "install" {
					installs++
					if mode == "hdc-failure" {
						return []byte("[Fail] install bundle failed"), nil
					}
					return []byte("[Success] Install package successfully."), nil
				}
				if program == "hdc" && strings.Contains(joined, "bm get -u") {
					if mode == "changed-device" {
						return []byte(strings.Repeat("b", 64)), nil
					}
					return []byte(strings.Repeat("a", 64)), nil
				}
				if program == "hdc" && strings.Contains(joined, "bm dump") {
					if mode == "wrong-installed-version" {
						return []byte(`{"name":"org.oheco.exportfixture","versionCode":11}`), nil
					}
					return []byte(`{"name":"org.oheco.exportfixture","versionCode":12}`), nil
				}
				return nil, nil
			}
			err := i.buildAndInstall(context.Background())
			if (err == nil) != (mode == "success") {
				t.Fatalf("%s: %v", mode, err)
			}
			if (mode == "stale" || mode == "missing-output" || mode == "unsigned" || mode == "wrong-profile" || mode == "build-failure" || mode == "wrong-app-version" || mode == "changed-device") && installs != 0 {
				t.Fatal("installed stale/ambiguous/failed output")
			}
			restored, _ := os.ReadFile(filepath.Join(root, "build-profile.json5"))
			if !bytes.Equal(original, restored) {
				t.Fatal("build mutated original profile")
			}
		})
	}
}
func TestProjectFailureRedactsSigningPasswords(t *testing.T) {
	err := projectFailure("build", []byte("failed with TOP-SECRET"), errors.New("exit 1"), []string{"TOP-SECRET"})
	if strings.Contains(err.Error(), "TOP-SECRET") || !strings.Contains(err.Error(), "<redacted>") {
		t.Fatal(err)
	}
}
