#!/usr/bin/env python3
"""Verify canonical HTTPS, legacy redirects and native domain-migration installs.

Default mode tests the built candidate's compiled default against the live v5
index. --published additionally verifies the deployed website/schema paths,
new install.sh and upgrade from the immutable 0.7.0 binary. Configure HTTPS_PROXY
and application-private TMPDIR; all state, homes and installations are isolated.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile

CANONICAL = "https://oheco.org"
# Compatibility input, not a current production default.
LEGACY = "https://oheco.github.io/oheco-packages"
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--oo", type=Path, default=Path(__file__).resolve().parent.parent / "build/oo")
parser.add_argument("--version", default="0.8.0")
parser.add_argument("--published", action="store_true")
args = parser.parse_args()
if sys.platform != "ohos":
    parser.error("run on the HarmonyOS host")
proxy = os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy")
if not proxy:
    parser.error("set HTTPS_PROXY to the required proxy")
curl_proxy = "socks5h://" + proxy[len("socks5://"):] if proxy.startswith("socks5://") else proxy
binary = args.oo.resolve()


def run(command, env):
    print("$", " ".join(str(x) for x in command), flush=True)
    result = subprocess.run([str(x) for x in command], env=env, input="", text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
    print(result.stdout, end="", flush=True)
    if result.returncode:
        raise RuntimeError(f"command exited {result.returncode}")
    return result.stdout


with tempfile.TemporaryDirectory(prefix="oo domain acceptance ") as temporary:
    work = Path(temporary)
    home = work / "private home"
    home.mkdir()
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("DSH_"):
            env.pop(key)
    env.pop("OHECO_INDEX_URL", None)
    env.update(HOME=str(home), OHECO_NO_AUTO_UPDATE="1", OHECO_NO_MODIFY_PATH="1",
               HTTPS_PROXY=proxy, HTTP_PROXY=proxy, NO_PROXY="127.0.0.1,localhost",
               no_proxy="127.0.0.1,localhost", XDG_CONFIG_HOME=str(work / "config"),
               XDG_CACHE_HOME=str(work / "cache"))

    def fetch(address, destination, *, direct=False, expected=None):
        print("GET", address, flush=True)
        result = subprocess.run(["curl", "-q", "--proxy", curl_proxy, "--connect-timeout", "10",
                                 "--max-time", "90", "--proto", "=https", "--proto-redir", "=https",
                                 "-fLsS", "-o", str(destination), "--write-out",
                                 "%{http_code}\t%{num_redirects}\t%{url_effective}", address],
                                env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=100)
        if result.returncode:
            raise RuntimeError(f"HTTPS fetch failed for {address}: {result.stderr}")
        status, redirects, effective = result.stdout.split("\t")
        assert status == "200", (address, status)
        assert effective.startswith("https://"), effective
        if direct:
            assert redirects == "0" and effective == address, (address, result.stdout)
        if expected:
            assert effective == expected, (address, effective)
        print("  PASS", status, f"redirects={redirects}", effective, flush=True)
        return destination.read_bytes()

    candidate_root = work / "candidate root"
    candidate_env = dict(env, OHECO_ROOT=str(candidate_root))
    assert "oo " + args.version + " (ohos/arm64)" in run([binary, "--version"], candidate_env)
    run([binary, "update"], candidate_env)
    cache = json.loads((candidate_root / "index/http.json").read_text())
    assert cache["url"] == CANONICAL + "/index/v5/index.json"
    assert cache["index_source"] == cache["url"]
    assert not (candidate_root / "state/installed.json").exists()
    index = json.loads(fetch(CANONICAL + "/index/v5/index.json", work / "index.json", direct=True))
    assert index["schema_version"] == 5
    run([binary, "search", "oheco"], candidate_env)
    if args.published:
        packages = {p["name"]: p for p in index["packages"]}
        assert packages["oheco"]["latest"]["ohos-arm64"] == args.version
        paths = ["/", "/install.sh", "/app.js", "/style.css"]
        paths += [f"/index/v{v}/index.json" for v in range(1, 6)]
        paths += ["/schema/index.schema.json", "/schema/package.schema.json"]
        paths += [f"/schema/v{v}/{name}.schema.json" for v in range(1, 5) for name in ("index", "package")]
        for i, path in enumerate(paths):
            canonical = fetch(CANONICAL + path, work / f"canonical-{i}", direct=True)
            legacy = fetch(LEGACY + path, work / f"legacy-{i}", expected=CANONICAL + path)
            assert canonical == legacy, f"legacy alias returned different content: {path}"
            if path.startswith("/schema/"):
                schema = json.loads(canonical)
                expected_base = LEGACY if path.startswith(tuple(f"/schema/v{v}/" for v in range(1, 5))) else CANONICAL
                assert schema["$id"] == expected_base + path
        html = (work / "canonical-0").read_text()
        assert 'rel="canonical" href="https://oheco.org/"' in html
        assert "curl -fsSL https://oheco.org/install.sh | zsh" in html
        installer = work / "canonical-1"
        fresh_root = work / "fresh root with spaces"
        fresh_env = dict(env, OHECO_ROOT=str(fresh_root))
        fresh_env["PATH"] = str(fresh_root / "bin") + os.pathsep + env["PATH"]
        run(["/usr/bin/zsh", installer], fresh_env)
        installed_oo = fresh_root / "bin/oo"
        assert "oo " + args.version in run([installed_oo, "--version"], fresh_env)
        assert json.loads((fresh_root / "index/http.json").read_text())["url"] == CANONICAL + "/index/v5/index.json"
        assert not (home / ".zshrc").exists(), "installer ignored isolated no-PATH-change setting"
        run([installed_oo, "install", "git-lfs", "-y"], fresh_env)
        run([fresh_root / "bin/git", "--version"], fresh_env)
        run([fresh_root / "bin/git-lfs", "version"], fresh_env)
        run([installed_oo, "remove", "git-lfs", "--autoremove", "-y"], fresh_env)

        old = next(v for v in packages["oheco"]["versions"] if v["version"] == "0.7.0")["artifacts"]["ohos-arm64"]
        old_archive = work / "old-0.7.0.tar.gz"
        data = fetch(old["url"], old_archive)
        assert len(data) == old["size"] and hashlib.sha256(data).hexdigest() == old["sha256"]
        old_unpacked = work / "old binary"
        old_unpacked.mkdir()
        with tarfile.open(old_archive) as archive:
            archive.extractall(old_unpacked, filter="data")
        upgrade_root = work / "upgrade root"
        upgrade_env = dict(env, OHECO_ROOT=str(upgrade_root))
        upgrade_env["PATH"] = str(upgrade_root / "bin") + os.pathsep + env["PATH"]
        old_binary = old_unpacked / "bin/oo"
        run([old_binary, "update"], upgrade_env)
        assert json.loads((upgrade_root / "index/http.json").read_text())["url"] == LEGACY + "/index/v5/index.json"
        run([old_binary, "install", "oheco@0.7.0"], upgrade_env)
        upgraded = upgrade_root / "bin/oo"
        run([upgraded, "install", "oheco"], upgrade_env)
        assert "oo " + args.version in run([upgraded, "--version"], upgrade_env)
        run([upgraded, "update"], upgrade_env)
        assert json.loads((upgrade_root / "index/http.json").read_text())["url"] == CANONICAL + "/index/v5/index.json"
        state = json.loads((upgrade_root / "state/installed.json").read_text())
        assert state["packages"]["oheco"]["active"] == args.version
        assert "0.7.0" in state["packages"]["oheco"]["versions"]
        run([upgraded, "remove", "oheco@0.7.0"], upgrade_env)
        print("PASS published install.sh, native package lifecycle, old-client upgrade and all legacy HTTPS aliases", flush=True)
    print("PASS compiled canonical default, live HTTPS index, and isolated state", flush=True)
