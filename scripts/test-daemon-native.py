#!/usr/bin/env python3
"""Native black-box acceptance for oo daemon (no third-party Python modules).

Usage: python3 scripts/test-daemon-native.py [build/oo-daemon]
All configuration, services and executable copies live below a private directory
in the real application TMPDIR. The daemon still receives that real TMPDIR, so
its Unix socket paths are not lengthened by the deliberately awkward test root.
"""

import argparse
import ctypes
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import traceback


FIXTURE = r'''import fcntl, json, os, pathlib, signal, subprocess, sys, time
mode, name = sys.argv[1:3]
events = pathlib.Path(os.environ["OO_NATIVE_EVENTS"])

def start_tick():
    return pathlib.Path("/proc/self/stat").read_text().rsplit(")", 1)[1].split()[19]

def event(kind, **extra):
    value = dict(kind=kind, name=name, mode=mode, pid=os.getpid(),
                 pgid=os.getpgrp(), sid=os.getsid(0), start=start_tick(),
                 monotonic_ns=time.monotonic_ns())
    value.update(extra)
    fd = os.open(events, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        os.write(fd, (json.dumps(value, ensure_ascii=False) + "\n").encode())
    finally:
        os.close(fd)

if mode == "grandchild":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    event("started")
    end = time.monotonic() + 45
    while time.monotonic() < end:
        time.sleep(0.05)
    sys.exit(0)

lease = None
if mode in ("tree", "natural", "tree-separate", "natural-separate"):
    lease_path = pathlib.Path(os.environ["OO_NATIVE_LEASES"]) / (name + ".lock")
    lease = os.open(lease_path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        event("OVERLAP")
        sys.exit(91)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)

payload = dict(name=name, pid=os.getpid(), argv=sys.argv[3:], cwd=os.getcwd(),
               value=os.environ.get("NATIVE_VALUE"), path=os.environ.get("PATH"),
               stdin_tty=os.isatty(0), stdout_tty=os.isatty(1), stderr_tty=os.isatty(2))
print("OUT " + json.dumps(payload, ensure_ascii=False), flush=True)
print("ERR " + json.dumps(payload, ensure_ascii=False), file=sys.stderr, flush=True)
event("started")

if mode == "fail":
    time.sleep(0.05)
    sys.exit(7)
if mode in ("tree", "natural", "tree-separate", "natural-separate"):
    child = subprocess.Popen([sys.executable, __file__, "grandchild", name],
                             stdin=subprocess.DEVNULL, pass_fds=(lease,),
                             process_group=0 if mode.endswith("-separate") else None)
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        try:
            lines = events.read_text().splitlines()
            if any(json.loads(line).get("pid") == child.pid for line in lines):
                break
        except (FileNotFoundError, json.JSONDecodeError):
            pass
        time.sleep(0.01)
    else:
        event("child-not-ready", child_pid=child.pid)
        sys.exit(92)
    if mode in ("natural", "natural-separate"):
        sys.exit(0)
else:
    stopped = False
    def stop(signum, frame):
        global stopped
        stopped = True
    signal.signal(signal.SIGTERM, stop)

end = time.monotonic() + 45
while time.monotonic() < end:
    if mode not in ("tree", "natural", "tree-separate", "natural-separate") and stopped:
        event("stopped")
        break
    time.sleep(0.025)
'''


class Failure(RuntimeError):
    pass


def check(condition, message):
    if not condition:
        raise Failure(message)


def process_info(pid):
    """PID identity from the kernel; a reused PID must never be signalled."""
    for attempt in range(6):
        try:
            fields = Path(f"/proc/{int(pid)}/stat").read_text().rsplit(")", 1)[1].split()
            return {"start": fields[19], "state": fields[0], "pgid": int(fields[2]),
                    "sid": int(fields[3])}
        except (FileNotFoundError, ProcessLookupError):
            return None
        except OSError as error:
            # HarmonyOS can transiently return EINVAL while a killed task exits.
            # Retry; do not treat that error as proof the PID is dead.
            if error.errno != errno.EINVAL or attempt == 5:
                raise
            time.sleep(.005)


def same_live_process(pid, start):
    info = process_info(pid)
    return bool(info and info["start"] == str(start) and info["state"] != "Z")


def wait_for(description, predicate, timeout=8):
    deadline = time.monotonic() + timeout
    latest = None
    while time.monotonic() < deadline:
        latest = predicate()
        if latest:
            return latest
        time.sleep(0.04)
    raise Failure(f"timed out after {timeout}s: {description}; last result={latest!r}")


def reap_known(pid):
    try:
        os.waitpid(pid, os.WNOHANG)
    except ChildProcessError:
        pass


class Case:
    def __init__(self, suite, name):
        self.suite = suite
        self.name = name
        self.directory = suite.directory / name
        self.root = self.directory / "root with spaces ' and $dollar"
        self.home = self.directory / "isolated home '$"
        self.zdot = self.directory / "isolated zsh '$"
        self.services = self.root / "services"
        self.events_path = self.directory / "fixture events '$.jsonl"
        self.leases = self.directory / "leases"
        self.fixture = self.directory / "service fixture '$.py"
        self.stable = self.root / "bin" / "oo"
        self.binary = suite.binary
        self.known = {}
        self.last_view = None
        for directory in (self.services, self.home, self.zdot, self.leases, self.stable.parent):
            directory.mkdir(parents=True, exist_ok=True)
        self.fixture.write_text(FIXTURE)
        self.stable.symlink_to(suite.binary)
        self.env = dict(os.environ)
        self.env.update(HOME=str(self.home), ZDOTDIR=str(self.zdot),
                        OHECO_ROOT=str(self.root), TMPDIR=str(suite.real_tmp),
                        OHECO_INDEX_URL="not-a-valid-index-url", OHECO_NO_AUTO_UPDATE="0",
                        OO_NATIVE_CASE=str(self.directory), OO_NATIVE_EVENTS=str(self.events_path),
                        OO_NATIVE_LEASES=str(self.leases), TERM="dumb")
        # Do not inherit configuration variables that can relocate Python caches
        # or inject a user-specific interpreter startup into the fixture.
        self.env.pop("PYTHONSTARTUP", None)
        self.env["PYTHONDONTWRITEBYTECODE"] = "1"

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        if exc is not None:
            self.diagnostics()
        try:
            self.cleanup()
            self.assert_no_index()
        except Exception as cleanup_error:
            if exc is None:
                raise
            print(f"CLEANUP FAIL {self.name}: {cleanup_error}", file=sys.stderr, flush=True)
        return False

    def own_path(self, path):
        # Check both the lexical path and any symlink target before removal.
        path = Path(path)
        check(path.absolute().is_relative_to(self.suite.directory),
              f"refusing removal outside test directory: {path}")
        check(path.resolve().is_relative_to(self.suite.directory),
              f"refusing removal of external symlink target: {path}")
        return path

    def cli(self, *args, allowed=(0,), timeout=15, binary=None):
        command = [str(binary or self.binary), "daemon", *map(str, args)]
        try:
            result = subprocess.run(command, env=self.env, stdin=subprocess.DEVNULL,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                    text=True, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            raise Failure(f"command timed out after {timeout}s: {command!r}; "
                          f"stdout={error.stdout!r}; stderr={error.stderr!r}") from error
        check(result.returncode in allowed,
              f"command {command!r} exited {result.returncode}, expected {allowed}; "
              f"stdout={result.stdout[-3000:]!r}; stderr={result.stderr[-3000:]!r}")
        return result

    def remember(self, pid, expected_start=None, direct_child=False):
        if not pid:
            return
        pid = int(pid)
        check(pid != os.getpid(), "daemon reported acceptance script PID")
        info = process_info(pid)
        if not info or (expected_start is not None and info["start"] != str(expected_start)):
            return
        if not direct_child:
            try:
                environment = Path(f"/proc/{pid}/environ").read_bytes().split(b"\0")
            except (FileNotFoundError, ProcessLookupError):
                return
            marker = ("OO_NATIVE_CASE=" + str(self.directory)).encode()
            check(marker in environment, f"PID {pid} does not belong to isolated case {self.name}")
        self.known[pid] = info["start"]

    def view(self, action="status", names=()):
        result = self.cli(action, *names, "--json", allowed=(0, 1) if action == "status" else (0,))
        try:
            view = json.loads(result.stdout)
        except json.JSONDecodeError as error:
            raise Failure(f"{action} did not return JSON: {result.stdout!r}; {result.stderr!r}") from error
        check(isinstance(view.get("running"), bool) and isinstance(view.get("services"), list),
              f"invalid {action} JSON shape: {view!r}")
        if view["running"]:
            check(isinstance(view.get("pid"), int) and view["pid"] > 0 and view.get("version"),
                  f"running daemon lacks PID/version: {view!r}")
            self.remember(view["pid"])
        for service in view["services"]:
            for key in ("pid", "runner_pid"):
                self.remember(service.get(key))
        self.last_view = view
        return view

    def service(self, name):
        view = self.view(names=(name,))
        matches = [s for s in view["services"] if s.get("name") == name]
        check(len(matches) == 1, f"missing or duplicate service {name}: {view!r}")
        return matches[0]

    def state(self, name, wanted, timeout=8):
        latest = {}
        def ready():
            nonlocal latest
            latest = self.service(name)
            return latest if latest.get("state") == wanted else None
        try:
            return wait_for(f"{name} becomes {wanted}", ready, timeout)
        except Failure as error:
            raise Failure(f"{error}; last service={latest!r}") from error

    def events(self, name=None, mode=None, kind=None):
        try:
            data = self.events_path.read_text()
        except FileNotFoundError:
            return []
        entries = []
        for line in data.splitlines():
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                # A concurrent append can expose only the final incomplete line.
                if not data.endswith("\n") and line == data.splitlines()[-1]:
                    continue
                raise
            self.remember(entry["pid"], entry["start"])
            if ((name is None or entry["name"] == name) and
                    (mode is None or entry["mode"] == mode) and
                    (kind is None or entry["kind"] == kind)):
                entries.append(entry)
        return entries

    def define(self, name, mode="steady", command=None, **overrides):
        value = dict(schema_version=1,
                     command=command or [str(self.suite.python), str(self.fixture), mode, name],
                     working_directory=str(self.home), environment={}, restart="no",
                     restart_delay="100ms", stop_timeout="350ms",
                     start_limit={"interval": "10s", "burst": 5})
        value.update(overrides)
        (self.services / (name + ".json")).write_text(json.dumps(value, ensure_ascii=False))

    def payloads(self, name, stream):
        path = self.services / "logs" / f"{name}.{stream}.log"
        try:
            lines = path.read_text().splitlines()
        except FileNotFoundError:
            return []
        prefix = "OUT " if stream == "stdout" else "ERR "
        check(all(line.startswith(prefix) for line in lines),
              f"{name} {stream} contains the other stream or unexpected output: {lines!r}")
        return [json.loads(line[len(prefix):]) for line in lines]

    def assert_no_index(self):
        check(not (self.root / "index").exists(),
              "daemon command created package index directory with invalid index URL")
        check(not (self.root / "state" / "index-update.lock").exists(),
              "daemon command entered background index updater")

    def signal_known(self, pid, signum):
        start = self.known.get(pid)
        check(start is not None, f"refusing to signal unknown PID {pid}")
        if same_live_process(pid, start):
            os.kill(pid, signum)

    def stopped_identities(self, entries):
        for entry in entries:
            reap_known(entry["pid"])
        return all(not same_live_process(e["pid"], e["start"]) for e in entries)

    def no_group(self, pgid):
        try:
            os.killpg(pgid, 0)
        except ProcessLookupError:
            return True
        return False

    def diagnostics(self):
        print(f"DETAIL {self.name}: last status={self.last_view!r}", file=sys.stderr)
        for path in (self.events_path, self.services / ".state" / "daemon.log"):
            try:
                print(f"DETAIL {path.name}:\n{path.read_text()[-6000:]}", file=sys.stderr)
            except (FileNotFoundError, OSError):
                pass

    def cleanup(self):
        # Always try the public shutdown first, even if the assertion failed
        # before a successful status query recorded the daemon PID.
        try:
            self.view()
        except Exception:
            pass
        self.events()
        shutdown_error = None
        try:
            shutdown = self.cli("shutdown", allowed=(0, 1), timeout=12, binary=self.suite.binary)
            if shutdown.returncode:
                shutdown_error = f"rc={shutdown.returncode}; stdout={shutdown.stdout!r}; stderr={shutdown.stderr!r}"
        except Exception as error:
            shutdown_error = str(error)
        # Endpoint inspection is an emergency cleanup aid only, never a passing
        # acceptance assertion. Its PID is still checked against the case env.
        endpoint = self.services / ".state" / "endpoint.json"
        try:
            self.remember(json.loads(endpoint.read_text()).get("pid"))
        except (FileNotFoundError, json.JSONDecodeError):
            pass
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            self.events()
            for pid in self.known:
                reap_known(pid)
            if not any(same_live_process(pid, start) for pid, start in self.known.items()):
                break
            time.sleep(0.04)
        survivors = [pid for pid, start in self.known.items() if same_live_process(pid, start)]
        # Kill only individually identified descendants. Never kill a guessed
        # process group or a PID read without its start-time/environment checks.
        for pid in survivors:
            self.signal_known(pid, signal.SIGKILL)
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            for pid in self.known:
                reap_known(pid)
            if not any(same_live_process(pid, start) for pid, start in self.known.items()):
                break
            time.sleep(0.04)
        alive = [pid for pid, start in self.known.items() if same_live_process(pid, start)]
        check(not alive, f"owned processes survived cleanup: {alive}")
        check(not survivors and shutdown_error is None,
              f"shutdown failed native cleanup; forced PIDs={survivors}; error={shutdown_error}")


def singleton(case):
    children = []
    try:
        for _ in range(12):
            children.append(subprocess.Popen([str(case.binary), "daemon"], env=case.env,
                                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                            stderr=subprocess.PIPE, text=True))
        for child in children:
            out, err = child.communicate(timeout=15)
            check(child.returncode == 0 and not out and not err,
                  f"concurrent launch failed: rc={child.returncode}, stdout={out!r}, stderr={err!r}")
    finally:
        for child in children:
            if child.poll() is None:
                child.kill()
            child.communicate(timeout=3)
    first = case.view()
    check(first["running"], "concurrent launches left no running daemon")
    daemon_pid = first["pid"]
    info = process_info(daemon_pid)
    check(info["sid"] == daemon_pid, f"daemon did not detach into its own session: {info!r}")
    for _ in range(3):
        result = case.cli()
        check(not result.stdout and not result.stderr, "duplicate daemon launch was not silent")
        check(case.view()["pid"] == daemon_pid, "duplicate launch replaced the daemon")


def lifecycle(case):
    for name in ("enabled", "manual"):
        case.define(name)
    case.cli("enable", "enabled")
    offline = case.view("list")
    check(not offline["running"], "enable started daemon")
    by_name = {s["name"]: s for s in offline["services"]}
    check(by_name["enabled"]["enabled"] and not by_name["manual"]["enabled"],
          f"offline enable state incorrect: {offline!r}")
    result = case.cli("start", "manual", allowed=(1,))
    check(result.stderr.strip(), "offline start failed without an explanatory error")
    check(not case.view("list")["running"], "offline start silently launched daemon")
    case.cli()
    case.state("enabled", "running")
    check(case.service("manual")["state"] == "inactive", "disabled service autostarted")
    case.cli("start", "manual")
    check(not case.state("manual", "running")["enabled"], "start changed enabled state")
    case.cli("stop", "enabled")
    check(case.state("enabled", "inactive")["enabled"], "stop changed enabled state")
    case.cli("shutdown")
    case.cli()
    enabled = case.state("enabled", "running")
    check(case.service("manual")["state"] == "inactive", "manual service persisted across relaunch")
    case.cli("disable", "enabled")
    check(case.service("enabled")["pid"] == enabled["pid"], "disable stopped a running service")
    case.cli("enable", "manual")
    check(case.service("manual")["state"] == "inactive", "enable immediately started service")
    case.cli("shutdown")
    case.cli()
    case.state("manual", "running")
    check(case.service("enabled")["state"] == "inactive", "disabled setting did not persist")


def streams_and_environment(case):
    cwd = case.directory / "cwd with spaces ' and $literal"
    custom_bin = cwd / "custom bin '$"
    custom_bin.mkdir(parents=True)
    (custom_bin / "native-python").symlink_to(case.suite.python)
    args = ["", "two words", "'quote'", "$NOT_EXPANDED", "*.json", "semi;colon", "line\nbreak", "中文"]
    value = "environment with spaces ' $literal"
    case.define("io", command=["native-python", str(case.fixture), "steady", "io", *args],
                working_directory=str(cwd), environment={"PATH": str(custom_bin), "NATIVE_VALUE": value})
    case.cli()
    case.cli("start", "io")
    first = case.state("io", "running")
    wait_for("first stdout/stderr entries", lambda: len(case.payloads("io", "stdout")) == 1 and
             len(case.payloads("io", "stderr")) == 1)
    expected_path = str(case.root / "bin") + os.pathsep + str(custom_bin)
    for stream in ("stdout", "stderr"):
        payload = case.payloads("io", stream)[0]
        check(payload["argv"] == args and payload["cwd"] == str(cwd) and
              payload["value"] == value and payload["path"] == expected_path,
              f"service argv/cwd/env/custom PATH changed: {payload!r}")
        check(not any(payload[key] for key in ("stdin_tty", "stdout_tty", "stderr_tty")),
              f"service inherited terminal descriptor: {payload!r}")
    snapshots = {stream: (case.services / "logs" / f"io.{stream}.log").read_bytes()
                 for stream in ("stdout", "stderr")}
    case.cli("start", "io")
    check(case.service("io")["pid"] == first["pid"], "idempotent start replaced running service")
    case.cli("restart", "io")
    second = case.state("io", "running")
    check(second["pid"] != first["pid"], "restart retained the old service process")
    wait_for("appended stdout/stderr after restart", lambda: len(case.payloads("io", "stdout")) == 2 and
             len(case.payloads("io", "stderr")) == 2)
    for stream, before in snapshots.items():
        after = (case.services / "logs" / f"io.{stream}.log").read_bytes()
        check(after.startswith(before) and len(after) > len(before), f"restart truncated {stream}")
    files = sorted(p.name for p in (case.services / "logs").iterdir())
    check(files == ["io.stderr.log", "io.stdout.log"], f"expected exactly two service logs: {files}")


def invalid_isolation(case):
    case.define("healthy")
    case.define("broken")
    case.cli("enable", "healthy", "broken")
    (case.services / "broken.json").write_text('{"schema_version":999,"command":["false"]}')
    case.cli()
    healthy = case.state("healthy", "running")
    failed = case.state("broken", "failed")
    check(failed.get("error"), "invalid enabled service lacked diagnostic")
    case.cli("status", "--json")  # Overall daemon health is independent of failed services.
    case.cli("status", "broken", "--json", allowed=(1,))
    case.cli("start", "broken", allowed=(1,))
    case.define("missing-command", command=[str(case.directory / "does-not-exist")])
    case.cli("start", "missing-command", allowed=(1,))
    check(case.service("healthy")["pid"] == healthy["pid"],
          "invalid service disturbed unrelated healthy service")


def restart_limits(case):
    case.define("limited", mode="fail", restart="on-failure", restart_delay="100ms",
                start_limit={"interval": "10s", "burst": 3})
    case.define("pending", mode="fail", restart="always", restart_delay="1200ms")
    case.cli()
    case.cli("start", "limited")
    failed = case.state("limited", "failed")
    attempts = case.events("limited", "fail", "started")
    check(len(attempts) == 3 and failed.get("starts") == 3 and "limit" in failed.get("error", "").lower(),
          f"restart limit did not stop exactly three attempts: status={failed!r}, attempts={attempts!r}")
    case.cli("start", "pending")
    case.state("pending", "restarting")
    before = len(case.events("pending", "fail", "started"))
    check(before == 1, f"pending retry fixture did not start once: {before}")
    case.cli("stop", "pending")
    case.state("pending", "inactive")
    deadline = time.monotonic() + 1.7
    while time.monotonic() < deadline:
        check(len(case.events("pending", "fail", "started")) == before,
              "manual stop did not cancel pending automatic restart")
        check(case.service("pending")["state"] == "inactive", "stopped retry left inactive state")
        time.sleep(0.08)
    limited = case.service("limited")
    check(limited["starts"] == 3, "rate-limited service kept retrying")


def crash_replacement(case):
    for name in ("alpha", "beta"):
        case.define(name, mode="tree" if name == "alpha" else "tree-separate", stop_timeout="700ms")
    case.cli("enable", "alpha", "beta")
    case.cli()
    for name in ("alpha", "beta"):
        case.state(name, "running")
        wait_for(f"{name} TERM-ignoring grandchild", lambda name=name:
                 len(case.events(name, "grandchild", "started")) == 1)
    previous = case.view()
    old_events = case.events(kind="started")
    check(len(old_events) == 4, f"expected two leaders and two grandchildren: {old_events!r}")
    old_groups = {e["pgid"] for e in old_events}
    check(len(old_groups) == 3 and len({e["sid"] for e in old_events}) == 2,
          f"expected two service sessions and an additional child group: {old_events!r}")
    case.signal_known(previous["pid"], signal.SIGKILL)
    # Confirm kernel death (kill is asynchronous), but give no grace period to
    # the runners: their TERM-ignoring descendants still need timeout/SIGKILL.
    wait_for("killed daemon exits", lambda: not same_live_process(
        previous["pid"], case.known[previous["pid"]]), timeout=2)
    reap_known(previous["pid"])
    case.cli()
    current = case.view()
    check(current["running"] and current["pid"] != previous["pid"], "daemon replacement did not start")
    for name in ("alpha", "beta"):
        case.state(name, "running")
        wait_for(f"replacement {name} grandchild", lambda name=name:
                 len(case.events(name, "grandchild", "started")) == 2)
    check(not case.events(kind="OVERLAP"), "replacement overlapped an old leader/grandchild generation")
    wait_for("all old service identities dead", lambda: case.stopped_identities(old_events))
    for pgid in old_groups:
        wait_for(f"old process group {pgid} removed", lambda pgid=pgid: case.no_group(pgid))
    check(len(case.events(kind="started")) == 8, "unexpected duplicate generations after replacement")


def broken_runner_report(case):
    case.define("broken-report", stop_timeout="250ms")
    case.cli("enable", "broken-report")  # Prepare persistent directories, not a daemon.
    helper = '''import fcntl,os,sys
lock=open(sys.argv[1],"a+b")
fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
image=open(sys.argv[2],"rb")
a=fcntl.fcntl(lock.fileno(),fcntl.F_DUPFD_CLOEXEC,10)
b=fcntl.fcntl(image.fileno(),fcntl.F_DUPFD_CLOEXEC,10)
os.dup2(a,3,inheritable=True)
os.dup2(b,4,inheritable=True)
os.close(a);os.close(b)
os.execve("/proc/self/fd/4",["oo","_service","broken-report"],os.environ)
'''
    lock_path = case.services / ".state/service-broken-report.lock"
    proc = subprocess.Popen([str(case.suite.python), "-c", helper, str(lock_path), str(case.suite.binary)],
                            env=case.env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE)
    case.remember(proc.pid, direct_child=True)
    proc.stdout.close()
    proc.stdout = None
    try:
        # Keep liveness stdin open, but break the report pipe before exec/start.
        # The runner must handle EPIPE and clean up, rather than die by SIGPIPE.
        proc.stdin.write((case.services / "broken-report.json").read_bytes())
        proc.stdin.flush()
        code = proc.wait(timeout=5)
        error = proc.stderr.read().decode(errors="replace")
        check(code == 0, f"runner exited before safe cleanup after broken report: {code}, {error}")
        wait_for("broken-report service cleaned", lambda: case.stopped_identities(case.events(kind="started")))
        with lock_path.open("r+b") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    finally:
        if proc.poll() is None:
            case.signal_known(proc.pid, signal.SIGKILL)
            proc.wait(timeout=3)
        proc.stdin.close()
        proc.stderr.close()


def runner_crash(case):
    case.define("runner-crash", mode="tree", restart="on-failure", stop_timeout="350ms")
    case.cli()
    case.cli("start", "runner-crash")
    original = case.state("runner-crash", "running")
    wait_for("runner-crash grandchild ready", lambda: len(case.events(mode="grandchild", kind="started")) == 1)
    old_events = case.events(kind="started")
    case.signal_known(original["runner_pid"], signal.SIGKILL)
    wait_for("runner failure restarts after cleanup", lambda: len(case.events(mode="tree", kind="started")) == 2)
    wait_for("replacement runner grandchild", lambda: len(case.events(mode="grandchild", kind="started")) == 2)
    current = case.state("runner-crash", "running")
    check(current["runner_pid"] != original["runner_pid"], "failed runner was not replaced")
    check(not case.events(kind="OVERLAP"), "runner failure restarted before cleaning old descendants")
    wait_for("old runner descendants gone", lambda: case.stopped_identities(old_events))
    for pgid in {entry["pgid"] for entry in old_events}:
        wait_for("old runner group removed", lambda pgid=pgid: case.no_group(pgid))


def natural_exit(case, mode="natural"):
    case.define("natural", mode=mode, stop_timeout="250ms")
    case.cli()
    case.cli("start", "natural")
    wait_for("natural leader and grandchild recorded", lambda: len(case.events(kind="started")) == 2)
    ended = case.state("natural", "inactive")
    check(ended.get("exit_code") == 0 and ended["starts"] == 1,
          f"natural leader exit not preserved: {ended!r}")
    entries = case.events(kind="started")
    wait_for("grandchild cleaned after natural leader exit", lambda: case.stopped_identities(entries))
    for pgid in {e["pgid"] for e in entries}:
        wait_for(f"natural-exit process group {pgid} removed", lambda pgid=pgid: case.no_group(pgid))


def separate_group_exit(case):
    natural_exit(case, "natural-separate")
    check(len({e["pgid"] for e in case.events(kind="started")}) == 2,
          "separate-group fixture did not actually create another process group")


def pty_shell(case):
    pid, terminal = pty.fork()
    if pid == 0:
        os.execve(str(case.suite.zsh), [str(case.suite.zsh), "-d", "-i"], case.env)
    # fork() itself identifies this owned child before execve installs case.env.
    case.remember(pid, direct_child=True)
    exited = False
    transcript = bytearray()
    try:
        # Exit immediately after startup. The autoload command is asynchronous
        # and must survive this shell exiting and its controlling PTY closing.
        os.write(terminal, b"print -r -- OO_NATIVE_PTY_LOADED; exit\n")
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            readable, _, _ = select.select([terminal], [], [], 0.04)
            if readable:
                try:
                    chunk = os.read(terminal, 65536)
                    transcript.extend(chunk)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
            done, status = os.waitpid(pid, os.WNOHANG)
            if done:
                exited = True
                check(os.waitstatus_to_exitcode(status) == 0,
                      f"PTY zsh failed: status={status}, transcript={transcript!r}")
                break
        check(exited, f"PTY zsh did not exit: {transcript!r}")
    finally:
        os.close(terminal)
        if not exited:
            case.signal_known(pid, signal.SIGKILL)
            os.waitpid(pid, 0)


def autoload(case):
    case.define("shell")
    case.cli("enable", "shell")
    original = b"# isolated existing zsh config without final newline"
    target = case.zdot / "actual rc '$"
    target.write_bytes(original)
    rc = case.zdot / ".zshrc"
    rc.symlink_to(target)
    printed = case.cli("autoload", "--print").stdout
    check(target.read_bytes() == original, "autoload --print modified zsh config")
    check("&!" in printed and "bin/oo" in printed and "status" not in printed,
          f"autoload is not one asynchronous stable-oo invocation: {printed!r}")
    case.cli("autoload")
    installed = target.read_bytes()
    case.cli("autoload")
    check(rc.is_symlink() and target.read_bytes() == installed,
          "autoload changed symlink or duplicated/rewrote installed block")
    check(installed.startswith(original + b"\n") and
          installed.count(b"# >>> oheco daemon autoload >>>") == 1,
          f"autoload did not preserve existing zsh config: {installed!r}")
    case.cli("status", "--quiet", allowed=(1,))
    delayed = case.directory / "delayed oo"
    delayed.write_text("#!/usr/bin/zsh\nsleep 3\nexec " + shlex.quote(str(case.suite.binary)) + ' "$@"\n')
    delayed.chmod(0o700)
    check(case.stable.is_symlink() and case.stable.resolve() == case.suite.binary, "unexpected stable test link")
    case.stable.unlink()
    case.stable.symlink_to(delayed)
    shell_started = time.monotonic()
    pty_shell(case)
    check(time.monotonic() - shell_started < 2, "zsh waited for the deliberately delayed autoload command")
    wait_for("autoload daemon after zsh exit and PTY close", lambda: case.view()["running"])
    case.own_path(case.stable).unlink()
    case.stable.symlink_to(case.suite.binary)
    running = case.state("shell", "running")
    daemon = case.view()["pid"]
    pty_shell(case)
    wait_for("same daemon after second terminal exit", lambda: case.view()["pid"] == daemon)
    check(case.service("shell")["pid"] == running["pid"], "second autoload restarted live service")
    case.cli("autoload", "--remove")
    check(rc.is_symlink() and target.read_bytes() == original, "autoload --remove did not restore config")
    case.cli("autoload", "--remove")
    check(target.read_bytes() == original, "second autoload removal modified user config")
    check(case.view()["pid"] == daemon, "autoload removal stopped daemon")


def pinned_executable(case):
    images = case.directory / "signed images '$"
    images.mkdir()
    first = images / "oo version one"
    second = images / "oo version two"
    shutil.copy2(case.suite.binary, first)
    shutil.copy2(case.suite.binary, second)
    first.chmod(0o700)
    second.chmod(0o700)
    original_identity = (first.stat().st_dev, first.stat().st_ino)
    check(original_identity != (second.stat().st_dev, second.stat().st_ino), "image copies share an inode")
    # Replace only this case's stable link. Resolve/verify the future unlink
    # targets explicitly; the original build binary is never removed or edited.
    check(case.stable.is_symlink() and case.stable.resolve() == case.suite.binary,
          "unexpected stable test link")
    case.stable.unlink()
    case.stable.symlink_to(first)
    case.binary = case.stable
    case.define("after-remove")
    case.define("after-switch")
    case.cli()
    daemon_pid = case.view()["pid"]
    case.own_path(case.stable).unlink()
    case.own_path(first).unlink()
    case.binary = case.suite.binary  # The stable entry is deliberately absent here.
    case.cli("start", "after-remove")
    after_remove = case.state("after-remove", "running")
    image = Path(f"/proc/{after_remove['runner_pid']}/exe").stat()
    check((image.st_dev, image.st_ino) == original_identity, "runner did not retain the removed original image")
    case.stable.symlink_to(second)
    case.binary = case.stable
    case.cli("start", "after-switch")
    after_switch = case.state("after-switch", "running")
    image = Path(f"/proc/{after_switch['runner_pid']}/exe").stat()
    check((image.st_dev, image.st_ino) == original_identity, "runner silently switched to the replacement image")
    check(case.view()["pid"] == daemon_pid, "executable removal/switch replaced live daemon")
    case.cli("restart", "after-remove")
    case.state("after-remove", "running")


def foreground(case):
    process = subprocess.Popen([str(case.binary), "daemon", "--foreground"], env=case.env,
                               stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True, start_new_session=True)
    try:
        case.remember(process.pid)
        wait_for("foreground daemon listens", lambda: case.view()["running"])
        check(case.view()["pid"] == process.pid and process.poll() is None,
              "--foreground detached or exited")
        case.cli()
        check(case.view()["pid"] == process.pid, "detached launch duplicated foreground daemon")
        case.cli("shutdown")
        out, err = process.communicate(timeout=5)
        check(process.returncode == 0, f"foreground shutdown failed: {out!r}; {err!r}")
    finally:
        if process.poll() is None:
            case.signal_known(process.pid, signal.SIGKILL)
        process.communicate(timeout=3)


CASES = [
    ("concurrent-singleton", singleton),
    ("enable-start-lifecycle", lifecycle),
    ("streams-cwd-env-path-argv", streams_and_environment),
    ("invalid-config-isolation", invalid_isolation),
    ("restart-limit-stop-cancels-retry", restart_limits),
    ("sigkill-replacement-whole-groups", crash_replacement),
    ("natural-leader-grandchild-cleanup", natural_exit),
    ("natural-leader-separate-group-cleanup", separate_group_exit),
    ("runner-sigkill-descendant-cleanup", runner_crash),
    ("broken-runner-report-cleanup", broken_runner_report),
    ("zsh-autoload-pty-survival", autoload),
    ("pinned-executable-removal-switch", pinned_executable),
    ("foreground-singleton-shutdown", foreground),
]


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("binary", nargs="?", type=Path,
                        default=Path(__file__).resolve().parents[1] / "build" / "oo-daemon")
    parser.add_argument("--case", action="append", choices=[name for name, _ in CASES], help="run only selected cases")
    args = parser.parse_args()
    selected = [(name, test) for name, test in CASES if not args.case or name in args.case]
    binary = args.binary.resolve()
    check(binary.is_file() and os.access(binary, os.X_OK),
          f"native executable does not exist or is not executable: {binary}; build/sign it first")
    configured_tmp = os.environ.get("TMPDIR")
    check(configured_tmp and Path(configured_tmp).is_absolute(), "set the real application TMPDIR first")
    real_tmp = Path(configured_tmp).resolve()
    check(real_tmp.is_dir() and str(real_tmp) != "/tmp", f"invalid application TMPDIR: {real_tmp}")
    zsh = shutil.which("zsh")
    check(zsh, "native zsh is required for PTY acceptance")
    # Orphans from detached daemons/runners are adopted by this test process,
    # allowing its own recorded descendants to be reaped during cleanup.
    libc = ctypes.CDLL(None, use_errno=True)
    check(libc.prctl(36, 1, 0, 0, 0) == 0,
          f"cannot enable child subreaper: errno={ctypes.get_errno()}")
    with tempfile.TemporaryDirectory(prefix="oo-daemon-native-", dir=real_tmp) as temporary:
        suite = argparse.Namespace(directory=Path(temporary).resolve(), real_tmp=real_tmp,
                                   binary=binary, python=Path(sys.executable).resolve(), zsh=Path(zsh).resolve())
        # TemporaryDirectory removes only this verified newly-created directory.
        check(suite.directory.parent == real_tmp and suite.directory.name.startswith("oo-daemon-native-"),
              f"unexpected temporary directory: {suite.directory}")
        check(suite.directory.stat().st_mode & 0o077 == 0, "test TMPDIR does not honor private permissions")
        for name, test in selected:
            started = time.monotonic()
            try:
                with Case(suite, name) as case:
                    test(case)
            except Exception as error:
                print(f"FAIL {name}: {error}", file=sys.stderr, flush=True)
                traceback.print_exc()
                return 1
            print(f"PASS {name} ({time.monotonic() - started:.2f}s)", flush=True)
    print(f"PASS all {len(selected)} native daemon cases", flush=True)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (Failure, OSError) as error:
        print(f"FAIL setup: {error}", file=sys.stderr, flush=True)
        sys.exit(1)
