#!/usr/bin/env python3
"""Native test for the installer's .zshrc configuration blocks.

Builds a small `oo` fixture archive, serves it over loopback HTTP, renders
install.sh from scripts/install.sh.tmpl and exercises the PATH / XDG-TMPDIR
behaviour: unattended runs, legacy PATH-line migration, and interactive
answers typed on a pty. Only the standard library is used.
"""

import hashlib
import http.server
import io
import os
import pty
import select
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
from pathlib import Path

HERE = Path(__file__).resolve().parent
TEMPLATE = HERE / "install.sh.tmpl"
ZSH = shutil.which("zsh") or "/usr/bin/zsh"
CACHE = "/data/storage/el2/base/haps/entry/cache"
CONFIG = "/data/storage/el2/base/haps/entry/files"
TEMP = "/data/storage/el2/base/haps/entry/temp"
PATH_MARKER = "# oheco: command path"
ENV_MARKER = "# oheco: private directories"

FAKE_OO = """#!/usr/bin/zsh
case $1 in
  _bootstrap)
    mkdir -p -- "$OHECO_ROOT/bin"
    cp -- "$0" "$OHECO_ROOT/bin/oo"
    chmod 755 -- "$OHECO_ROOT/bin/oo"
    ;;
esac
exit 0
"""


def check(condition, message):
    if not condition:
        raise AssertionError(message)
    print(f"  PASS {message}", flush=True)


class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


def serve(directory):
    handler = lambda *a, **kw: Quiet(*a, directory=str(directory), **kw)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def build_archive(directory):
    archive = directory / "oheco-fixture.tar.gz"
    payload = FAKE_OO.encode()
    with tarfile.open(archive, "w:gz") as tar:
        info = tarfile.TarInfo("bin/oo")
        info.size = len(payload)
        info.mode = 0o755
        info.mtime = 0
        tar.addfile(info, io.BytesIO(payload))
    return archive, hashlib.sha256(archive.read_bytes()).hexdigest()


def render_installer(url, digest, destination):
    text = TEMPLATE.read_text()
    for key, value in (("@@VERSION@@", "'0.0.0-fixture'"), ("@@URL@@", f"'{url}'"),
                       ("@@SHA256@@", f"'{digest}'"), ("@@MANIFEST@@", "{}")):
        text = text.replace(key, value)
    destination.write_text(text)
    destination.chmod(0o755)


def base_env(home, **extra):
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("DSH_"):
            env.pop(key)
    for key in ("HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
                "OHECO_ROOT", "OHECO_ASSUME_YES", "OHECO_NO_MODIFY_PATH",
                "OHECO_NO_MODIFY_ENV", "ZDOTDIR"):
        env.pop(key, None)
    env.update(HOME=str(home), NO_PROXY="127.0.0.1,localhost",
               no_proxy="127.0.0.1,localhost")
    env.update(extra)
    return env


def run_unattended(installer, env):
    result = subprocess.run([ZSH, str(installer)], env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            timeout=120)
    print(result.stdout, end="", flush=True)
    check(result.returncode == 0, f"installer exited {result.returncode}")
    return result.stdout


def run_interactive(installer, env, answers):
    """Run the installer on a pty and type the answers at each [Y/n] prompt."""
    pid, fd = pty.fork()
    if pid == 0:
        try:
            os.execve(ZSH, [ZSH, str(installer)], env)
        finally:
            os._exit(127)
    output = b""
    sent = 0
    try:
        while True:
            ready, _, _ = select.select([fd], [], [], 30)
            if not ready:
                check(False, "installer did not answer on the pty")
            try:
                chunk = os.read(fd, 4096)
            except OSError:
                break
            if not chunk:
                break
            output += chunk
            while sent < output.count(b"[Y/n]") and sent < len(answers):
                os.write(fd, answers[sent].encode() + b"\n")
                sent += 1
    finally:
        os.close(fd)
        _, status = os.waitpid(pid, 0)
    text = output.decode(errors="replace")
    print(text, end="", flush=True)
    check(os.waitstatus_to_exitcode(status) == 0, "interactive installer exited 0")
    check(sent == len(answers), f"installer asked {sent} questions, expected {len(answers)}")
    return text


def legacy_line(bin_path):
    """Reproduce the exact line older installers appended to .zshrc."""
    return subprocess.run([ZSH, "-f", "-c", 'print -rn -- "export PATH=${(q)1}:\\$PATH"',
                           "legacy", str(bin_path)], text=True,
                          stdout=subprocess.PIPE, check=True).stdout


def rc_of(home):
    rc = home / ".zshrc"
    return rc.read_text() if rc.exists() else ""


def path_block(reference="$HOME/.oheco/bin"):
    return f'case ":$PATH:" in\n  *":{reference}:"*) ;;\n  *) export PATH="{reference}:$PATH" ;;\nesac\n'


def check_blocks(rc, *, path=True, env=True):
    check(rc.count(PATH_MARKER) == (1 if path else 0), f"PATH marker count ({rc.count(PATH_MARKER)})")
    check(path_block() in rc if path else path_block() not in rc, "PATH case block")
    check(rc.count(ENV_MARKER) == (1 if env else 0), f"XDG marker count ({rc.count(ENV_MARKER)})")
    for value in (f"export XDG_CACHE_HOME={CACHE}", f"export XDG_CONFIG_HOME={CONFIG}",
                  f"export TMPDIR={TEMP}"):
        check((value in rc) == env, f"{value} written={env}")


def main():
    with tempfile.TemporaryDirectory(prefix="oheco-config-test.") as temporary:
        work = Path(temporary)
        served = work / "served"
        served.mkdir()
        archive, digest = build_archive(served)
        server = serve(served)
        url = f"http://127.0.0.1:{server.server_address[1]}/{archive.name}"
        installer = work / "install.sh"
        render_installer(url, digest, installer)
        subprocess.run([ZSH, "-n", str(installer)], check=True)
        print("PASS installer renders and parses", flush=True)

        print("== unattended OHECO_ASSUME_YES writes both blocks ==", flush=True)
        home = work / "home-unattended"
        home.mkdir()
        env = base_env(home, OHECO_ASSUME_YES="1")
        run_unattended(installer, env)
        rc = rc_of(home)
        check_blocks(rc)
        found = subprocess.run([ZSH, "-f", "-c", 'source "$HOME/.zshrc"; command -v oo'],
                               env=env, text=True, stdout=subprocess.PIPE).stdout.strip()
        check(found == str(home / ".oheco/bin/oo"), f"oo found on PATH ({found})")

        print("== a second run changes nothing ==", flush=True)
        before = rc
        run_unattended(installer, env)
        check(rc_of(home) == before, "reinstall left .zshrc untouched")

        print("== without a terminal nothing is written ==", flush=True)
        home = work / "home-pipe"
        home.mkdir()
        run_unattended(installer, base_env(home))
        check(rc_of(home) == "", "piped install wrote no .zshrc")

        print("== the legacy PATH line is upgraded in place ==", flush=True)
        home = work / "home legacy"
        home.mkdir()
        # Earlier installers wrote the resolved, shell-quoted path.
        legacy = legacy_line(home / ".oheco/bin")
        (home / ".zshrc").write_text(f"# user config\n\n{PATH_MARKER}\n{legacy}\n")
        run_unattended(installer, base_env(home, OHECO_ASSUME_YES="1"))
        rc = rc_of(home)
        check(legacy not in rc, "legacy export line replaced")
        check("# user config" in rc, "existing configuration preserved")
        check_blocks(rc)

        print("== the two switches are independent ==", flush=True)
        home = work / "home-path-off"
        home.mkdir()
        run_unattended(installer, base_env(home, OHECO_ASSUME_YES="1", OHECO_NO_MODIFY_PATH="1"))
        check_blocks(rc_of(home), path=False)

        home = work / "home-env-off"
        home.mkdir()
        run_unattended(installer, base_env(home, OHECO_ASSUME_YES="1", OHECO_NO_MODIFY_ENV="1"))
        check_blocks(rc_of(home), env=False)

        print("== interactive answers are honoured per question ==", flush=True)
        home = work / "home-interactive-no"
        home.mkdir()
        run_interactive(installer, base_env(home), ["n", "n"])
        check(rc_of(home) == "", "answering no to both wrote nothing")

        home = work / "home-interactive-path"
        home.mkdir()
        run_interactive(installer, base_env(home), ["", "n"])
        check_blocks(rc_of(home), env=False)

        home = work / "home-interactive-both"
        home.mkdir()
        run_interactive(installer, base_env(home), ["y", "y"])
        check_blocks(rc_of(home))

        server.shutdown()
        print("PASS installer PATH / XDG-TMPDIR prompts, migration and idempotence", flush=True)


if __name__ == "__main__":
    try:
        main()
    except AssertionError as error:
        print(f"FAIL {error}", flush=True)
        sys.exit(1)
