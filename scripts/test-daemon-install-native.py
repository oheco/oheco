#!/usr/bin/env python3
"""Validate the daemon release archive/official-index upgrade on HarmonyOS.

Before publishing: --archive dist/oheco-0.12.0-ohos-arm64.tar.gz
After publishing: --published
Both modes first install the real previous release through the official index.
All state, shell configuration and services remain inside application TMPDIR.
"""
import argparse
import functools
import hashlib
import http.server
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--oo', type=Path, default=Path(__file__).resolve().parents[1] / 'build/oo')
parser.add_argument('--version', default='0.12.0')
parser.add_argument('--previous', default='0.11.0')
mode = parser.add_mutually_exclusive_group(required=True)
mode.add_argument('--archive', type=Path)
mode.add_argument('--published', action='store_true')
args = parser.parse_args()
if sys.platform != 'ohos':
    parser.error('run on HarmonyOS')
proxy = os.environ.get('HTTPS_PROXY') or os.environ.get('https_proxy')
if not proxy:
    parser.error('set the required HTTPS_PROXY')
binary = args.oo.resolve()
base = Path(os.environ['TMPDIR']).resolve()


def wait_for(what, condition):
    end = time.monotonic() + 8
    while time.monotonic() < end:
        if condition():
            return
        time.sleep(.05)
    raise RuntimeError('timed out: ' + what)


with tempfile.TemporaryDirectory(prefix='oo-daemon-install-', dir=base) as temporary:
    work = Path(temporary).resolve()
    assert work.parent == base and work.name.startswith('oo-daemon-install-')
    root = work / "installed root '$ with spaces"
    home = work / 'home'
    home.mkdir()
    env = dict(os.environ, OHECO_ROOT=str(root), HOME=str(home), ZDOTDIR=str(home),
               OHECO_NO_AUTO_UPDATE='1', HTTPS_PROXY=proxy, HTTP_PROXY=proxy,
               NO_PROXY='127.0.0.1,localhost', no_proxy='127.0.0.1,localhost')
    env.pop('OHECO_INDEX_URL', None)
    env['PATH'] = str(root / 'bin') + os.pathsep + os.environ.get('PATH', '')
    installed = root / 'bin/oo'
    server = thread = None

    def run(executable, *arguments, allowed=(0,)):
        result = subprocess.run([str(executable), *map(str, arguments)], env=env,
                                stdin=subprocess.DEVNULL, capture_output=True,
                                text=True, timeout=600)
        if result.returncode not in allowed:
            raise RuntimeError(f'{arguments}: rc={result.returncode}\n{result.stdout}\n{result.stderr}')
        if result.stdout:
            print(result.stdout.strip(), flush=True)
        return result

    try:
        assert 'oo ' + args.version in run(binary, '--version').stdout
        run(binary, 'update')
        cache = json.loads((root / 'index/http.json').read_text())
        assert cache['url'] == 'https://oheco.org/index/v6/index.json'
        index = json.loads((root / 'index/index.json').read_text())
        package = next(p for p in index['packages'] if p['name'] == 'oheco')
        assert index['schema_version'] == 6
        if args.published:
            assert package['latest']['ohos-arm64'] == args.version
        run(binary, 'install', 'oheco@' + args.previous, '-y')
        assert 'oo ' + args.previous in run(installed, '--version').stdout
        print('PASS real official index and previous-release download', flush=True)

        if args.archive:
            archive = args.archive.resolve()
            payload = archive.read_bytes()
            digest = hashlib.sha256(payload).hexdigest()
            served = work / 'served'
            served.mkdir()
            (served / 'candidate.tar.gz').write_bytes(payload)

            class QuietHandler(http.server.SimpleHTTPRequestHandler):
                def log_message(self, *unused):
                    pass

            server = http.server.ThreadingHTTPServer(('127.0.0.1', 0),
                functools.partial(QuietHandler, directory=str(served)))
            address = f'http://127.0.0.1:{server.server_port}'
            package['latest'] = {'ohos-arm64': args.version}
            package['versions'] = [{
                'version': args.version,
                'artifacts': {'ohos-arm64': {
                    'url': address + '/candidate.tar.gz', 'sha256': digest,
                    'size': len(payload), 'format': 'tar.gz', 'strip_components': 0,
                    'binaries': {'oo': 'bin/oo'}}}}]
            index['packages'] = [package]
            (served / 'index.json').write_text(json.dumps(index))
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            env['OHECO_INDEX_URL'] = address + '/index.json'
        run(installed, 'update')
        run(installed, 'install', 'oheco', '-y')
        assert 'oo ' + args.version in run(installed, '--version').stdout
        if args.archive:
            assert (root / 'packages/oheco' / args.version / 'bin/oo').read_bytes() == binary.read_bytes()
        print('PASS previous client upgrades and switches to new signed client', flush=True)

        services = root / 'services'
        services.mkdir(exist_ok=True)
        cfg = {'schema_version': 1, 'command': [sys.executable, '-u', '-c',
            'import sys,time;print("stdout");print("stderr",file=sys.stderr);time.sleep(60)'],
            'restart': 'no', 'stop_timeout': '250ms'}
        (services / 'acceptance.json').write_text(json.dumps(cfg))
        run(installed, 'daemon', 'enable', 'acceptance')
        run(installed, 'daemon')
        stdout = services / 'logs/acceptance.stdout.log'
        stderr = services / 'logs/acceptance.stderr.log'
        wait_for('installed service output', lambda: stdout.exists() and stderr.exists()
                 and stdout.read_text().count('stdout\n') == 1 and stderr.read_text().count('stderr\n') == 1)
        view = json.loads(run(installed, 'daemon', 'status', '--json').stdout)
        assert view['running'] and view['version'] == args.version
        assert view['services'][0]['enabled'] and view['services'][0]['state'] == 'running'
        run(installed, 'daemon', 'restart', 'acceptance')
        wait_for('append after installed restart', lambda: stdout.read_text().count('stdout\n') == 2)
        rc = home / '.zshrc'
        original = b'# retained isolated shell configuration'
        rc.write_bytes(original)
        run(installed, 'daemon', 'autoload')
        run(installed, 'daemon', 'autoload')
        assert rc.read_text().count('# >>> oheco daemon autoload >>>') == 1
        run(installed, 'daemon', 'autoload', '--remove')
        assert rc.read_bytes() == original
        print('PASS installed daemon lifecycle, append logs and isolated autoload', flush=True)

        run(installed, 'switch', 'oheco', args.previous)
        assert 'oo ' + args.previous in run(installed, '--version').stdout
        run(binary, 'daemon', 'restart', 'acceptance')
        run(installed, 'switch', 'oheco', args.version)
        assert 'oo ' + args.version in run(installed, '--version').stdout
        # The daemon must keep launching runners after all installed executable
        # paths are removed, and uninstall must retain user service definitions.
        run(binary, 'remove', 'oheco', '--all', '-y')
        assert not installed.exists() and (services / 'acceptance.json').is_file()
        run(binary, 'daemon', 'restart', 'acceptance')
        run(binary, 'daemon', 'stop', 'acceptance')
        run(binary, 'daemon', 'shutdown')
        assert not json.loads(run(binary, 'daemon', 'list', '--json').stdout)['running']
        assert not json.loads((root / 'state/installed.json').read_text())['packages']
        print('PASS offline version switch, uninstall, pinned runner and retained definitions', flush=True)
    finally:
        if (services if 'services' in locals() else root / 'services').exists():
            result = subprocess.run([str(binary), 'daemon', 'shutdown'], env=env,
                stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=15)
            if result.returncode:
                raise RuntimeError('isolated daemon cleanup failed: ' + result.stderr)
        if server:
            server.shutdown()
            server.server_close()
            thread.join(timeout=3)
print('PASS official-index install' if args.published else 'PASS release archive acceptance', flush=True)
