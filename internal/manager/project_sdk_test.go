package manager

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func projectSDKTestWrite(t *testing.T, filename, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func projectSDKTestFixture(t *testing.T, source string) *projectInstaller {
	t.Helper()
	root := t.TempDir()
	clt := filepath.Join(root, "sdk")
	work := filepath.Join(root, "work with spaces \"quoted\" \\ unicode-中")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	projectSDKTestWrite(t, filepath.Join(clt, projectSDKCacheRelative), source)
	return &projectInstaller{work: work, tools: projectTools{clt: clt, env: setLanguageEnv(os.Environ(), "NODE_OPTIONS", "--no-warnings")}}
}

func projectSDKTestNode(t *testing.T, env []string, script string, args ...string) ([]byte, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js with public module.registerHooks is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, append([]string{"-e", script, "--"}, args...)...)
	command.Env = env
	command.Dir = t.TempDir()
	return command.CombinedOutput()
}

const projectSDKTestModule = `const hvigor_common_1 = { isHarmony: () => true };
` + projectSDKRootFunction + `
exports.root = getRootDir;
exports.other = () => "/storage/Users/currentUser";
`

func TestProjectSDKEnvironmentAndHook(t *testing.T) {
	i := projectSDKTestFixture(t, projectSDKTestModule)
	original := append([]string(nil), i.tools.env...)
	env, err := i.buildEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(i.tools.env, original) {
		t.Fatal("buildEnvironment modified the prepared environment")
	}
	withoutOptions := func(env []string) []string {
		var result []string
		for _, entry := range env {
			if !strings.HasPrefix(entry, "NODE_OPTIONS=") {
				result = append(result, entry)
			}
		}
		return result
	}
	if !reflect.DeepEqual(withoutOptions(env), withoutOptions(original)) {
		t.Fatal("buildEnvironment changed variables other than NODE_OPTIONS")
	}
	preloads, err := os.ReadDir(i.work)
	if err != nil || len(preloads) != 1 {
		t.Fatalf("preloads = %v, error = %v", preloads, err)
	}
	info, err := os.Stat(filepath.Join(i.work, preloads[0].Name()))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("preload is not 0600: %v, %v", info, err)
	}
	other := filepath.Join(t.TempDir(), "cache.js")
	projectSDKTestWrite(t, other, projectSDKTestModule)
	target := filepath.Join(i.tools.clt, projectSDKCacheRelative)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	output, err := projectSDKTestNode(t, env, `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const original = fs.readdirSync;
const root = process.argv[3];
const target = require(process.argv[1]);
assert.equal(target.root(root), process.platform === 'openharmony' ? root : '/storage/Users/currentUser');
assert.equal(target.other(), '/storage/Users/currentUser');
assert.equal(require(process.argv[2]).root(root), '/storage/Users/currentUser');
assert.equal(fs.readdirSync, original);
console.log('exact URL and platform hook verified');
`, target, other, t.TempDir())
	if err != nil {
		t.Fatalf("actual Node require hook: %v\n%s", err, output)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("SDK module changed on disk")
	}
	// Returned entries must not alias the caller's slice, even without appends.
	env[0] = "TEST_CHANGED=1"
	if !reflect.DeepEqual(i.tools.env, original) {
		t.Fatal("returned environment aliases the original slice")
	}
}

func TestProjectSDKFutureAndUnknownShapes(t *testing.T) {
	t.Run("future SDK", func(t *testing.T) {
		i := projectSDKTestFixture(t, `function getRootDir(rootDir){return rootDir} exports.root=getRootDir;`)
		env, err := i.buildEnvironment()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(env, i.tools.env) {
			t.Fatal("future SDK was given an unnecessary preload")
		}
		output, err := projectSDKTestNode(t, env, `require('node:assert/strict').equal(require(process.argv[1]).root('caller-root'), 'caller-root')`, filepath.Join(i.tools.clt, projectSDKCacheRelative))
		if err != nil {
			t.Fatalf("future SDK load: %v\n%s", err, output)
		}
	})
	t.Run("unrelated HOME literal", func(t *testing.T) {
		i := projectSDKTestFixture(t, `function getRootDir(rootDir){return rootDir} exports.home="/storage/Users/currentUser";`)
		if _, err := i.buildEnvironment(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown affected shape", func(t *testing.T) {
		i := projectSDKTestFixture(t, strings.Replace(projectSDKTestModule, "return(0,", "return (0,", 1))
		if _, err := i.buildEnvironment(); err == nil || !strings.Contains(err.Error(), "unsupported SDK getRootDir shape") {
			t.Fatalf("expected useful shape diagnostic, got %v", err)
		}
	})
	for _, future := range []bool{false, true} {
		name := "changed after preparation"
		if future {
			name = "fixed after preparation"
		}
		t.Run(name, func(t *testing.T) {
			i := projectSDKTestFixture(t, projectSDKTestModule)
			env, err := i.buildEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			source := strings.Replace(projectSDKTestModule, "return(0,", "return (0,", 1)
			if future {
				source = `function getRootDir(e){return e} exports.root=getRootDir;`
			}
			projectSDKTestWrite(t, filepath.Join(i.tools.clt, projectSDKCacheRelative), source)
			output, err := projectSDKTestNode(t, env, `
const assert = require('node:assert/strict');
if (process.platform !== 'openharmony') process.exit(0);
if (process.argv[2] === 'true') assert.equal(require(process.argv[1]).root('future'), 'future');
else assert.throws(() => require(process.argv[1]), /unsupported getRootDir shape with hardcoded HOME/);
`, filepath.Join(i.tools.clt, projectSDKCacheRelative), map[bool]string{false: "false", true: "true"}[future])
			if err != nil {
				t.Fatalf("load-time shape gate: %v\n%s", err, output)
			}
		})
	}
}

func TestProjectSDKTargetValidation(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "oversized", "symlink escape"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "sdk", projectSDKCacheRelative)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				projectSDKTestWrite(t, target, strings.Repeat("x", projectSDKSourceLimit+1))
			case "symlink escape":
				outside := filepath.Join(t.TempDir(), "cache.js")
				projectSDKTestWrite(t, outside, projectSDKTestModule)
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			}
			i := &projectInstaller{work: root, tools: projectTools{clt: filepath.Join(root, "sdk")}}
			if _, err := i.buildEnvironment(); err == nil {
				t.Fatalf("accepted %s SDK target", kind)
			}
		})
	}
}

// This probe exercises the installed SDK's exported cache APIs, not a copy of
// checkFileWithCase. The peer dependency is resolved through a public hook just
// as Hvigor's launcher binds its engine, without importing any CLI internals.
func TestProjectSDKInstalledCache(t *testing.T) {
	clt := os.Getenv("OHECO_TEST_CLT")
	if clt == "" {
		t.Skip("set OHECO_TEST_CLT to probe an installed SDK")
	}
	clt = canonicalProjectPath(clt)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	file := filepath.Join(project, "src", "CaseFile.ets")
	projectSDKTestWrite(t, file, "export const value = 1;\n")
	// A second independent root proves that the caller controls the boundary.
	projectSDKTestWrite(t, filepath.Join(root, "second", "src", "Second.ets"), "second\n")
	projectSDKTestWrite(t, filepath.Join(root, "sdk-modules", "External.ets"), "external\n")
	blocked := filepath.Join(project, "blocked")
	projectSDKTestWrite(t, filepath.Join(blocked, "Blocked.ets"), "blocked\n")
	if err := os.Chmod(blocked, 0300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0700)
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0000); err != nil {
		t.Fatal(err)
	}
	i := &projectInstaller{work: root, tools: projectTools{clt: clt, env: setLanguageEnv(os.Environ(), "NODE_OPTIONS", "--no-warnings")}}
	env, err := i.buildEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	// Compare an actual ordinary fs EACCES in fresh processes with/without the
	// preload. On Harmony this is the exact ancestor in the original failure.
	permissionScript := `
const fs = require('node:fs');
const target = process.platform === 'openharmony' ? '/data/storage/el2' : process.argv[1];
try { fs.readdirSync(target); console.log('readable'); }
catch (error) { console.log(JSON.stringify({code:error.code, syscall:error.syscall, path:error.path})); }
`
	before, err := projectSDKTestNode(t, i.tools.env, permissionScript, denied)
	if err != nil {
		t.Fatalf("baseline fs probe: %v\n%s", err, before)
	}
	after, err := projectSDKTestNode(t, env, permissionScript, denied)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("ordinary fs behavior changed: %v\nbefore %s\nafter %s", err, before, after)
	}
	output, err := projectSDKTestNode(t, env, `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { registerHooks } = require('node:module');
const clt = process.argv[1], root = process.argv[2];
registerHooks({resolve(specifier, context, nextResolve) {
  return nextResolve(specifier === '@ohos/hvigor' ? path.join(clt, 'hvigor/hvigor') : specifier, context);
}});
const cache = require(path.join(clt, '`+projectSDKCacheRelative+`'));
const originalReaddir = fs.readdirSync;
const directories = [];
fs.readdirSync = function(directory, ...args) {
  directories.push(String(directory));
  return originalReaddir.call(this, directory, ...args);
};
function read(file, rootDir, sdkModulePaths = []) {
  return new Promise((resolve, reject) => cache.readCachedFile(file, (error, data) => error ? reject(error) : resolve(data), {caseSensitiveCheck:true, rootDir, sdkModulesPath:sdkModulePaths}));
}
function reset() { cache.clearStepAddressingCache(); directories.length = 0; }
(async () => {
  const project = path.join(root, 'project');
  const file = path.join(project, 'src/CaseFile.ets');
  assert.equal(String(await read(file, project)), 'export const value = 1;\n');
  assert.deepEqual(directories, [path.join(project, 'src'), project]);
  assert.equal(cache.caseErrorSet.size, 0);
  reset();
  const second = path.join(root, 'second');
  assert.equal(String(await read(path.join(second, 'src/Second.ets'), second)), 'second\n');
  assert.deepEqual(directories, [path.join(second, 'src'), second]);
  reset();
  const sdk = path.join(root, 'sdk-modules');
  assert.equal(String(await read(path.join(sdk, 'External.ets'), project, [sdk])), 'external\n');
  assert.deepEqual(directories, [sdk]);
  reset();
  const src = path.join(project, 'src');
  assert.equal(String(await read(file, src)), 'export const value = 1;\n');
  assert.deepEqual(directories, [src]);
  reset();
  // Node stat accepts a Buffer path. Its actual bytes name the existing file,
  // while the SDK's toString() case check sees the deliberately wrong spelling.
  // This exercises the real directory listing on case sensitive TMPDIR too.
  const wrong = path.join(src, 'casefile.ets');
  const buffer = Buffer.from(file);
  buffer.toString = () => wrong;
  const value = await new Promise((resolve, reject) => cache.isFileCached(buffer, (error, value) => error ? reject(error) : resolve(value), {caseSensitiveCheck:true, rootDir:project, sdkModulesPath:[], originImportPath:wrong}));
  assert.equal(value, true);
  assert.equal(cache.caseErrorSet.size, 1);
  assert.match([...cache.caseErrorSet][0], /casefile\.ets.*Please check the case/);
  assert.deepEqual(directories, [src]);
  reset();
  await assert.rejects(read(path.join(project, 'missing.ets'), project), error => error.code === 'ENOENT');
  const forbidden = process.platform === 'openharmony' ? '/data/storage/el2' : path.join(root, 'denied');
  let direct;
  try { originalReaddir(forbidden); } catch (error) { direct = error; }
  if (process.platform === 'openharmony') assert.equal(direct.code, 'EACCES');
  // This project directory permits file access, but genuinely forbids listing.
  // SDK case checking must preserve this EACCES, rather than swallowing it.
  const blocked = path.join(project, 'blocked');
  let blockedError;
  try { originalReaddir(blocked); } catch (error) { blockedError = error; }
  if (process.platform === 'openharmony') assert.equal(blockedError.code, 'EACCES');
  if (blockedError) {
    const blockedFile = path.join(blocked, 'Blocked.ets');
    assert.equal(String(fs.readFileSync(blockedFile)), 'blocked\n');
    await assert.rejects(read(blockedFile, project), error => error.code === blockedError.code && error.path === blocked);
  }
  fs.readdirSync = originalReaddir;
  console.log('installed SDK: caller roots, sdkModulePaths, reads, case checks and fs errors verified');
})().catch(error => { console.error(error); process.exitCode = 1; });
`, clt, root)
	if err != nil {
		t.Fatalf("installed SDK hook probe: %v\n%s", err, output)
	}
	t.Logf("ordinary fs error unchanged: %s", bytes.TrimSpace(after))
	t.Logf("%s", bytes.TrimSpace(output))
}
