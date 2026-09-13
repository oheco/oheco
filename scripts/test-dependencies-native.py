#!/usr/bin/env python3
"""Isolated, offline CLI acceptance for native dependencies and npm/pip routing.

Run on HarmonyOS after scripts/build-ohos.sh. Set TMPDIR to application-private
storage (the shared /storage tree does not preserve all POSIX permission bits).
No user npm prefix, Python environment, oheco root, or configuration is modified.
"""
import base64
import csv
import hashlib
import http.server
import io
import json
import os
import pty
import select
import signal
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import venv
import zipfile

OO = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parent.parent / "build/oo").resolve()
PLATFORM = "ohos-arm64"
BLOBS = {}
INDEX = {}
DOWNLOADS = []


def run(argv, env, *, success=True):
    result = subprocess.run([str(x) for x in argv], env=env, text=True, input="", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
    print("$", " ".join(str(x) for x in argv), flush=True)
    print(result.stdout, end="", flush=True)
    if (result.returncode == 0) != success:
        raise AssertionError(f"unexpected exit {result.returncode}: {argv}")
    return result.stdout


def tar_bytes(files):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as tar:
        for name, (data, mode) in files.items():
            if isinstance(data, str):
                data = data.encode()
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = mode
            info.mtime = 0
            tar.addfile(info, io.BytesIO(data))
    return output.getvalue()


def file_descriptor(base, filename, data):
    path = "/artifacts/" + filename
    BLOBS[path] = data
    return {"filename": filename, "url": base + path, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def package(name, version, *, manager="oheco"):
    p = {"schema_version": 5 if manager == "oheco" else 3, "name": name,
         "description": "Isolated CLI acceptance fixture", "upstream": "https://example.com/fixture",
         "repository": "https://github.com/oheco/oheco", "maintainers": [{"github": "oheco"}],
         "license": "MIT", "latest": {PLATFORM: version}, "versions": []}
    if manager != "oheco":
        p.update(package_manager=manager, package_name=name)
    return p


def native_version(base, name, version, command, dependencies=()):
    script = "#!/usr/bin/zsh -f\necho " + command + "@" + version + "\n"
    if dependencies:
        script += "oo-e2e-runtime\n"
    data = tar_bytes({"bin/" + command: (script, 0o755)})
    descriptor = file_descriptor(base, name + "-" + version + ".tar.gz", data)
    descriptor.pop("filename")
    descriptor.update(format="tar.gz", strip_components=0, binaries={command: "bin/" + command})
    v = {"version": version, "upstream_version": version, "artifacts": {PLATFORM: descriptor}}
    if dependencies:
        v["dependencies"] = list(dependencies)
    return v


def wheel(base, name, module, requirements=()):
    normalized = name.replace("-", "_")
    dist = normalized + "-1.0.0.dist-info/"
    metadata = f"Metadata-Version: 2.1\nName: {name}\nVersion: 1.0.0\nRequires-Python: >=3.8\n"
    metadata += "".join("Requires-Dist: " + req + "\n" for req in requirements)
    metadata += "\n"  # Terminate the RFC 822 metadata header block.
    files = {module + ".py": b"VALUE = 42\n", dist + "METADATA": metadata.encode(),
             dist + "WHEEL": b"Wheel-Version: 1.0\nGenerator: oo-e2e\nRoot-Is-Purelib: true\nTag: py3-none-any\n"}
    records = io.StringIO()
    writer = csv.writer(records, lineterminator="\n")
    for path, data in files.items():
        digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
        writer.writerow((path, "sha256=" + digest, len(data)))
    writer.writerow((dist + "RECORD", "", ""))
    files[dist + "RECORD"] = records.getvalue().encode()
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
        for path, data in files.items():
            archive.writestr(path, data)
    artifact = file_descriptor(base, normalized + "-1.0.0-py3-none-any.whl", output.getvalue())
    artifact["requires_python"] = ">=3.8"
    if requirements:
        artifact["requires_dist"] = list(requirements)
    p = package(name, "1.0.0", manager="pip")
    p["versions"] = [{"version": "1.0.0", "pip_artifacts": [artifact]}]
    return p


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path == "/index/v5/index.json":
            data = json.dumps(INDEX).encode()
        elif self.path in BLOBS:
            DOWNLOADS.append(self.path)
            data = BLOBS[self.path]
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    global INDEX
    if not OO.is_file():
        raise SystemExit("build/oo is missing; build and sign the native client first")
    if not shutil.which("npm") or not shutil.which("node"):
        raise SystemExit("npm/node are required for this acceptance (no silent skip)")
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    base = f"http://127.0.0.1:{server.server_port}"
    runtime = package("oo-e2e-runtime", "2.0.0")
    runtime["versions"] = [native_version(base, runtime["name"], v, runtime["name"]) for v in ("1.0.0", "2.0.0")]
    dep = {"name": runtime["name"], "constraint": ">=1.0.0 <2.0.0", "version_basis": "upstream"}
    app = package("oo-e2e-app", "1.0.0")
    app["versions"] = [native_version(base, app["name"], "1.0.0", app["name"], [dep])]
    other = package("oo-e2e-other", "1.0.0")
    other["versions"] = [native_version(base, other["name"], "1.0.0", other["name"], [dep])]
    npm = package("oo-e2e-npm", "1.0.0", manager="npm")
    manifest = {"name": npm["name"], "version": "1.0.0", "bin": {"oo-e2e-npm": "bin/cli.js"},
                "scripts": {"install": "exit 93"}}
    data = tar_bytes({"package/package.json": (json.dumps(manifest), 0o644),
                      "package/bin/cli.js": ("#!/usr/bin/env node\nconsole.log('npm fixture works');\n", 0o755)})
    artifact = file_descriptor(base, "oo-e2e-npm-1.0.0.tgz", data)
    artifact["package_json"] = manifest
    npm["versions"] = [{"version": "1.0.0", "npm_artifacts": artifact}]
    pip_dep = wheel(base, "oo-e2e-python-dep", "oo_e2e_python_dep")
    pip_root = wheel(base, "oo-e2e-python-root", "oo_e2e_python_root", ["oo-e2e-python-dep==1.0.0"])
    INDEX = {"schema_version": 5, "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
             "packages": [runtime, app, other, npm, pip_dep, pip_root]}
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="oheco acceptance ") as work:
            work = Path(work)
            root = work / "oo root with spaces"
            env = os.environ.copy()
            env.update(OHECO_ROOT=str(root), OHECO_INDEX_URL=base + "/index/v5/index.json", OHECO_NO_AUTO_UPDATE="1",
                       HTTP_PROXY="http://127.0.0.1:9", HTTPS_PROXY="http://127.0.0.1:9", NO_PROXY="127.0.0.1,localhost")
            env["PATH"] = str(root / "bin") + os.pathsep + env["PATH"]
            def oo(*args, success=True, selected_env=None):
                return run([OO, *args], selected_env or env, success=success)
            assert "ohos/arm64" in oo("--version"), "this acceptance must run the OHOS binary"
            oo("update")
            output = oo("search")
            assert "MANAGER" in output and "<由 npm 管理>" in output and "<由 pip 管理>" in output
            oo("install", app["name"], other["name"], "--dry-run")
            assert not DOWNLOADS and not (root / "state/installed.json").exists()
            oo("install", app["name"], other["name"], success=False)
            assert not DOWNLOADS
            # Entire native batch is staged before committing: a bad later root
            # must not leave the already-downloaded runtime installed.
            bad_path = "/artifacts/oo-e2e-app-1.0.0.tar.gz"
            good = BLOBS[bad_path]
            BLOBS[bad_path] = b"corrupt archive"
            oo("install", app["name"], "-y", success=False)
            assert not (root / "packages/oo-e2e-runtime/1.0.0").exists()
            BLOBS[bad_path] = good
            oo("install", app["name"], other["name"], "-y")
            state = json.loads((root / "state/installed.json").read_text())
            assert state["schema_version"] == 2
            assert state["packages"][runtime["name"]]["active"] == "1.0.0"
            assert state["packages"][runtime["name"]]["versions"]["1.0.0"]["automatic"]
            output = run([root / "bin/oo-e2e-app"], env)
            assert "oo-e2e-runtime@1.0.0" in output
            assert "requires oo-e2e-runtime >=1.0.0 <2.0.0" in oo("info", app["name"])
            # A real terminal prompt must stop on SIGTERM without requiring a
            # later newline, and must not delete the package after cancellation.
            master, slave = pty.openpty()
            prompt_process = subprocess.Popen([str(OO), "remove", app["name"]], env=env, stdin=slave, stdout=slave, stderr=slave)
            os.close(slave)
            try:
                prompt = b""
                deadline = time.monotonic() + 10
                while b"[y/N]" not in prompt:
                    if time.monotonic() >= deadline:
                        raise AssertionError("removal did not reach its terminal confirmation")
                    readable, _, _ = select.select([master], [], [], 0.2)
                    if readable:
                        prompt += os.read(master, 4096)
                prompt_process.send_signal(signal.SIGTERM)
                assert prompt_process.wait(timeout=5) != 0, "cancelled removal succeeded"
                assert (root / "packages/oo-e2e-app/1.0.0").exists()
                print("PASS SIGTERM cancels terminal confirmation without removing packages", flush=True)
            finally:
                if prompt_process.poll() is None:
                    prompt_process.kill()
                    prompt_process.wait()
                os.close(master)
            oo("install", runtime["name"] + "@2.0.0", "--no-switch", "-y")
            oo("switch", runtime["name"], "2.0.0", success=False)
            oo("remove", runtime["name"] + "@1.0.0", "-y", success=False)
            output = oo("remove", app["name"], "--autoremove", "-y")
            assert "still required by oo-e2e-other@1.0.0" in output
            assert (root / "packages/oo-e2e-runtime/1.0.0").exists()
            oo("remove", other["name"], "--autoremove", "-y")
            assert not (root / "packages/oo-e2e-runtime/1.0.0").exists()
            oo("remove", runtime["name"] + "@2.0.0", "-y")
            # Source overrides are rejected before a native root is installed.
            oo("install", app["name"], npm["name"], "-y", "--", "--registry=https://example.com", success=False)
            assert not (root / "packages/oo-e2e-app/1.0.0").exists()
            oo("install", npm["name"], pip_root["name"], "--", "--prefix", str(work / "unused"), success=False)
            npm_prefix = work / "npm global prefix"
            # oo injects no scope: ask for the global layout explicitly.
            oo("install", npm["name"], "-y", "--", "--global", "--prefix", str(npm_prefix))
            assert (npm_prefix / "lib/node_modules/oo-e2e-npm/package.json").exists(), "npm did not use global mode"
            assert "npm fixture works" in run([npm_prefix / "bin/oo-e2e-npm"], env)
            # A deliberately selected, isolated Python environment is safe for
            # actual default-prefix install/uninstall and needs no pip download.
            python_home = work / "python selected environment"
            venv.EnvBuilder(with_pip=False, system_site_packages=True, symlinks=True).create(python_home)
            python = python_home / "bin/python3"
            pyenv = env.copy()
            pyenv["OHECO_PYTHON"] = str(python)
            run([python, "-m", "pip", "--version"], pyenv)
            oo("install", pip_root["name"], "-y", selected_env=pyenv)
            run([python, "-c", "import oo_e2e_python_root, oo_e2e_python_dep; assert oo_e2e_python_root.VALUE == oo_e2e_python_dep.VALUE == 42"], pyenv)
            state = json.loads((root / "state/installed.json").read_text())
            assert not state["packages"], "external packages acquired native receipts"
            assert "<由 npm 管理>" in oo("search", npm["name"])
            assert npm["name"] not in oo("list")
            # Removal has a local index for name mapping but no live source.
            server.shutdown()
            server.server_close()
            thread.join()
            oo("remove", npm["name"], "-y", "--", "--global", "--prefix", str(npm_prefix))
            assert not (npm_prefix / "lib/node_modules/oo-e2e-npm").exists()
            oo("remove", pip_root["name"], "-y", selected_env=pyenv)
            run([python, "-c", "import importlib.util, oo_e2e_python_dep; assert importlib.util.find_spec('oo_e2e_python_root') is None"], pyenv)
            oo("remove", pip_dep["name"], "-y", selected_env=pyenv)
            assert not json.loads((root / "state/installed.json").read_text())["packages"]
            print("PASS native batch dependencies, rollback, shared removal, offline lifecycle, npm global, pip selected environment, and delegated queries", flush=True)
    finally:
        if thread.is_alive():
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    main()
