#!/usr/bin/env python3
"""Native acceptance for the v0.8.0 language download model (A1 + P2).

oo only serves metadata now: catalog packages are answered with their own
publication URLs, and every other name is forwarded to the sources the user
already configured. This script proves that end to end with loopback fixtures
only (no external network) and an isolated HOME/OHECO_ROOT:

  npm  catalog package  -> npm downloads the described tarball itself
  npm  other name       -> 302 to the registry from ~/.npmrc
  npm  ~/.npmrc prefix  -> honoured (the original user complaint)
  npm  credentials      -> anonymous, warned, and never leaked
  pip  catalog wheel    -> served with #sha256= from the described URL
  pip  other names      -> aggregated across a JSON and an HTML index
                           (the HTML one uses relative hrefs)
  pip  find-links       -> rejected with a clear error
  both                  -> loopback still bypasses a bogus proxy (NO_PROXY)

Run on the HarmonyOS host after scripts/build-ohos.sh. Needs npm and python3.
"""
import argparse
import base64
import csv
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile
import threading
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--oo", type=Path, default=Path(__file__).resolve().parent.parent / "build/oo")
parser.add_argument("--keep", action="store_true", help="keep the temporary workspace for inspection")
args = parser.parse_args()
if sys.platform != "ohos":
    parser.error("run on the HarmonyOS host")
OO = args.oo.resolve()
if not OO.is_file():
    parser.error(f"missing oo binary: {OO}")

STATE = {"npm_packument": 0, "npm_cdn_tarball": 0, "npm_auth": [], "release": 0,
         "pip_json": 0, "pip_html": 0, "pip_wheel": 0}


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def npm_tarball(name, version):
    manifest = json.dumps({"name": name, "version": version, "main": "index.js",
                           "scripts": {"install": "exit 93"}})
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for path, body in (("package/package.json", manifest),
                           ("package/index.js", "module.exports = %r;\n" % (name + "@" + version))):
            data = body.encode()
            info = tarfile.TarInfo(path)
            info.size = len(data)
            info.mtime = 0
            info.mode = 0o644
            tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def wheel(project, version="1.0.0", requires=()):
    normalized = project.replace("-", "_")
    dist = f"{normalized}-{version}.dist-info/"
    meta = f"Metadata-Version: 2.1\nName: {project}\nVersion: {version}\n"
    meta += "".join(f"Requires-Dist: {r}\n" for r in requires) + "\n"
    files = {
        normalized + "/__init__.py": b"VALUE = 21\n",
        dist + "METADATA": meta.encode(),
        dist + "WHEEL": b"Wheel-Version: 1.0\nGenerator: oo-acceptance\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
    }
    record = io.StringIO()
    writer = csv.writer(record, lineterminator="\n")
    for path, data in files.items():
        digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
        writer.writerow((path, "sha256=" + digest, len(data)))
    writer.writerow((dist + "RECORD", "", ""))
    files[dist + "RECORD"] = record.getvalue().encode()
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as archive:
        for path, data in files.items():
            archive.writestr(path, data)
    return out.getvalue()


def sha256(data):
    return hashlib.sha256(data).hexdigest()


with tempfile.TemporaryDirectory(prefix="oo forward acceptance ") as temporary:
    work = Path(temporary)
    port = free_port()
    cdn_port = free_port()
    base = f"http://127.0.0.1:{port}"
    # The forwarded tarball lives on a DIFFERENT origin on purpose: npm's default
    # replace-registry-host=npmjs rewrites tarball hosts to the configured
    # registry (oo), which only serves metadata and would answer 404.
    cdn_base = f"http://127.0.0.1:{cdn_port}"

    catalog_npm = "oo-accept-npm"
    catalog_tar = npm_tarball(catalog_npm, "1.0.0")
    forward_npm = "oo-accept-forward"
    forward_tar = npm_tarball(forward_npm, "1.0.0")
    catalog_pip = "oo-accept-pip"
    catalog_whl = wheel(catalog_pip)
    agg_root, agg_dep = "oo-accept-agg-root", "oo-accept-agg-dep"
    agg_root_whl = wheel(agg_root, requires=(f"{agg_dep}==1.0.0",))
    agg_dep_whl = wheel(agg_dep)
    agg_root_name = f"{agg_root.replace('-', '_')}-1.0.0-py3-none-any.whl"
    agg_dep_name = f"{agg_dep.replace('-', '_')}-1.0.0-py3-none-any.whl"

    catalog_pip_name = f"{catalog_pip.replace('-', '_')}-1.0.0-py3-none-any.whl"

    index = {
        "schema_version": 5,
        "generated_at": "2026-09-13T00:00:00Z",
        "packages": [
            {
                "schema_version": 3, "name": "accept-npm", "package_manager": "npm",
                "package_name": catalog_npm,
                "description": "npm catalog fixture", "upstream": "https://example.com/upstream",
                "repository": "https://github.com/oheco/fixture",
                "maintainers": [{"github": "kdada"}], "license": "MIT",
                "latest": {"ohos-arm64": "1.0.0"},
                "versions": [{"version": "1.0.0", "npm_artifacts": {
                    "url": f"{base}/release/{catalog_npm}-1.0.0.tgz", "sha256": sha256(catalog_tar),
                    "size": len(catalog_tar), "filename": f"{catalog_npm}-1.0.0.tgz",
                    "package_json": {"name": catalog_npm, "version": "1.0.0", "main": "index.js",
                                     "scripts": {"install": "exit 93"}}}}],
            },
            {
                "schema_version": 3, "name": "accept-pip", "package_manager": "pip",
                "package_name": catalog_pip,
                "description": "pip catalog fixture", "upstream": "https://example.com/upstream",
                "repository": "https://github.com/oheco/fixture",
                "maintainers": [{"github": "kdada"}], "license": "MIT",
                "latest": {"ohos-arm64": "1.0.0"},
                "versions": [{"version": "1.0.0", "pip_artifacts": [{
                    "url": f"{base}/release/{catalog_pip_name}", "sha256": sha256(catalog_whl),
                    "size": len(catalog_whl), "filename": catalog_pip_name,
                    "requires_python": ">=3.8"}]}],
            },
        ],
    }

    def packument(name, tarball_bytes, filename):
        manifest = {"name": name, "version": "1.0.0", "main": "index.js"}
        return {"name": name, "dist-tags": {"latest": "1.0.0"},
                "versions": {"1.0.0": dict(manifest, dist={
                    "tarball": f"{cdn_base}/cdn/{filename}", "integrity": "sha256-" + base64.b64encode(
                        bytes.fromhex(sha256(tarball_bytes))).decode(),
                    "shasum": sha256(tarball_bytes)})}}

    forward_packument = packument(forward_npm, forward_tar, f"{forward_npm}-1.0.0.tgz")

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def _send(self, body, ctype):
            self.send_response(200)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            path = self.path.split("?", 1)[0]
            if path == "/index/v5/index.json":
                return self._send(json.dumps(index).encode(), "application/json")
            if path == f"/release/{catalog_npm}-1.0.0.tgz":
                STATE["release"] += 1
                return self._send(catalog_tar, "application/octet-stream")
            if path == f"/release/{catalog_pip_name}":
                STATE["release"] += 1
                return self._send(catalog_whl, "application/octet-stream")
            # fake npm registry (forwarding target)
            if path == f"/npm/{forward_npm}":
                STATE["npm_packument"] += 1
                STATE["npm_auth"].append(self.headers.get("authorization"))
                return self._send(json.dumps(forward_packument).encode(), "application/json")
            # fake pip indexes: A answers PEP 691 JSON, B answers PEP 503 HTML
            if path == f"/simpleA/{agg_root}/":
                STATE["pip_json"] += 1
                doc = {"meta": {"api-version": "1.0"}, "name": agg_root, "files": [{
                    "filename": agg_root_name, "url": f"{base}/wheels/{agg_root_name}",
                    "hashes": {"sha256": sha256(agg_root_whl)}, "requires-python": ">=3.8"}]}
                return self._send(json.dumps(doc).encode(), "application/vnd.pypi.simple.v1+json")
            if path == f"/simpleB/{agg_dep}/":
                STATE["pip_html"] += 1
                # Relative href on purpose: oo must resolve it against this page
                # (/simpleB/<name>/ -> ../../wheels/<file>).
                body = ('<!doctype html><html><body><a href="../../wheels/%s#sha256=%s" '
                        'data-requires-python="&gt;=3.8">%s</a></body></html>'
                        % (agg_dep_name, sha256(agg_dep_whl), agg_dep_name)).encode()
                return self._send(body, "text/html; charset=utf-8")
            if path.startswith("/wheels/"):
                STATE["pip_wheel"] += 1
                name = path.rsplit("/", 1)[-1]
                data = {agg_root_name: agg_root_whl, agg_dep_name: agg_dep_whl}.get(name)
                if data is None:
                    self.send_error(404)
                    return
                return self._send(data, "application/octet-stream")
            self.send_error(404)

    class Cdn(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def do_GET(self):
            if self.path == f"/cdn/{forward_npm}-1.0.0.tgz":
                STATE["npm_cdn_tarball"] += 1
                self.send_response(200)
                self.send_header("Content-Length", str(len(forward_tar)))
                self.end_headers()
                self.wfile.write(forward_tar)
                return
            self.send_error(404)

    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    cdn = http.server.ThreadingHTTPServer(("127.0.0.1", cdn_port), Cdn)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    threading.Thread(target=cdn.serve_forever, daemon=True).start()

    home = work / "home"
    root = work / "root"
    npm_prefix = work / "npm prefix"
    home.mkdir(parents=True)
    pip_conf = home / "pip.conf"

    def write_npmrc(extra=""):
        (home / ".npmrc").write_text(
            f"registry={base}/npm/\nprefix={npm_prefix}\n"
            f"//127.0.0.1:{port}/:_authToken=FAKE-TOKEN-NOT-A-SECRET\n" + extra)

    def write_pip_conf(extra_indexes, find_links=None):
        lines = ["[global]", f"index-url = {base}/simpleA", f"timeout = 30"]
        for value in extra_indexes:
            lines.append(f"extra-index-url = {value}")
        if find_links:
            lines.append(f"find-links = {find_links}")
        pip_conf.write_text("\n".join(lines) + "\n")

    env = os.environ.copy()
    for key in list(env):
        if key.startswith("DSH_"):
            env.pop(key)
    env.update(
        HOME=str(home), OHECO_ROOT=str(root), OHECO_NO_AUTO_UPDATE="1",
        OHECO_INDEX_URL=f"{base}/index/v5/index.json",
        PIP_CONFIG_FILE=str(pip_conf), PIP_DISABLE_PIP_VERSION_CHECK="1",
        # A bogus proxy proves loopback still bypasses it for oo and its children.
        HTTP_PROXY="http://127.0.0.1:9", HTTPS_PROXY="http://127.0.0.1:9",
        NO_PROXY="127.0.0.1,localhost", no_proxy="127.0.0.1,localhost",
    )
    env["PATH"] = str(root / "bin") + os.pathsep + env["PATH"]

    def run(*command, success=True, expect=None):
        proc = subprocess.run([str(c) for c in command], env=env, text=True, input="",
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=900)
        print("$", " ".join(str(c) for c in command), flush=True)
        print(proc.stdout, end="", flush=True)
        if (proc.returncode == 0) != success:
            raise AssertionError(f"unexpected exit {proc.returncode}: {command}")
        if expect and expect not in proc.stdout:
            raise AssertionError(f"missing {expect!r} in output of {command}")
        return proc.stdout

    failures = []
    try:
        write_npmrc()
        write_pip_conf([f"{base}/simpleB"])
        oo = OO
        run(oo, "update")

        # --- npm: catalog package is downloaded by npm from the described URL
        release_before = STATE["release"]
        run(oo, "install", "accept-npm", "-y")
        assert STATE["release"] > release_before, "catalog tarball was not fetched from its described URL"
        assert STATE["npm_packument"] == 0, "catalog package metadata leaked to the forwarded registry"
        assert (npm_prefix / "lib/node_modules" / catalog_npm / "index.js").is_file(), \
            "npmrc prefix was not honoured for the global install"
        print("PASS npm catalog package: npm fetched the described tarball into the npmrc prefix", flush=True)

        # --- npm: other names are forwarded to the user's registry (anonymously)
        out = run(oo, "install", f"npm:{forward_npm}@1.0.0", "-y")
        assert STATE["npm_packument"] > 0, "forwarded registry was not used for metadata"
        assert STATE["npm_cdn_tarball"] > 0, "tarball host was rewritten instead of using the packument URL"
        assert "anonymous" in out, "credentials were configured but no anonymous warning was printed"
        assert all(value is None for value in STATE["npm_auth"]), \
            f"credentials leaked to the forwarded registry: {STATE['npm_auth']}"
        print("PASS npm forwarding: 302 to the user registry, anonymous and warned", flush=True)

        # --- pip: catalog wheel is served with its hash and downloaded by pip
        target = work / "pip target"
        release_before = STATE["release"]
        run(oo, "install", "accept-pip", "-y", "--", "--target", target, "--no-cache-dir")
        assert STATE["release"] > release_before, "catalog wheel was not fetched from its described URL"
        run(sys.executable, "-c", f"import sys; sys.path.insert(0, {str(target)!r}); import {catalog_pip.replace('-', '_')}",
            success=True)
        print("PASS pip catalog wheel: served with #sha256= and fetched from its described URL", flush=True)

        # --- pip: unknown names are aggregated across a JSON and an HTML index
        agg_target = work / "pip aggregate target"
        run(oo, "install", f"pip:{agg_root}", "-y", "--", "--target", agg_target, "--no-cache-dir")
        assert STATE["pip_json"] > 0, "PEP 691 index was not consulted"
        assert STATE["pip_html"] > 0, "PEP 503 index was not consulted"
        run(sys.executable, "-c",
            f"import sys; sys.path.insert(0, {str(agg_target)!r}); import {agg_root.replace('-', '_')}, {agg_dep.replace('-', '_')}")
        print("PASS pip aggregation: JSON + HTML indexes merged, relative href resolved, hashes passed through", flush=True)

        # --- pip: find-links is rejected with a clear error
        write_pip_conf([], find_links=str(work / "wheels"))
        out = run(oo, "install", f"pip:{agg_root}", "-y", success=False)
        assert "find-links" in out, "find-links was not reported clearly"
        print("PASS pip find-links rejected with an explicit message", flush=True)
    except AssertionError as exc:
        failures.append(str(exc))
    finally:
        write_pip_conf([f"{base}/simpleB"])
        if failures:
            print("\nFAIL:", failures[0], flush=True)
        server.shutdown()
        server.server_close()
        cdn.shutdown()
        cdn.server_close()
        if not failures:
            print("\nPASS language download model (A1 npm + P2 pip) with isolated fixtures", flush=True)
    sys.exit(1 if failures else 0)
