#!/usr/bin/env python3
"""Verify released oo and source exports through the official index, in isolation.

Run only after v0.13.0 and its v6 Pages index have been published. This verifies
ordinary Godot/Blender exports and install dry-runs; signing/device installation
is covered by the separate native acceptance script before publication.
Requires HarmonyOS, TMPDIR and the parent's HTTPS_PROXY configuration.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile


INDEX_URL = 'https://oheco.org/index/v6/index.json'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--oo', required=True)
    parser.add_argument('--report-dir', required=True)
    args = parser.parse_args()
    if sys.platform != 'ohos':
        parser.error('run on HarmonyOS')
    proxy = os.environ.get('HTTPS_PROXY') or os.environ.get('https_proxy')
    if not proxy:
        parser.error('set HTTPS_PROXY through the parent task before running')
    candidate = str(Path(args.oo).resolve())
    report = Path(args.report_dir).resolve()
    report.mkdir(parents=True, exist_ok=True)
    commands = []
    base = Path(os.environ['TMPDIR']).resolve()
    with (report / 'published.log').open('w') as log:
        with tempfile.TemporaryDirectory(prefix='oo-export-published-', dir=base) as work:
            root = Path(work).resolve()
            assert root.parent == base and root.name.startswith('oo-export-published-')
            installation = root / 'installation with spaces'
            home, temporary, config, cache = (root / name for name in ('home', 'tmp', 'config', 'cache'))
            for directory in (home, temporary, config, cache):
                directory.mkdir(mode=0o700)
            env = dict(os.environ, OHECO_ROOT=str(installation), OHECO_INDEX_URL=INDEX_URL,
                       HOME=str(home), ZDOTDIR=str(home), TMPDIR=str(temporary),
                       XDG_CONFIG_HOME=str(config), XDG_CACHE_HOME=str(cache),
                       OHECO_NO_AUTO_UPDATE='1', HTTP_PROXY=proxy, HTTPS_PROXY=proxy,
                       http_proxy=proxy, https_proxy=proxy,
                       NO_PROXY='localhost,127.0.0.1,::1', no_proxy='localhost,127.0.0.1,::1',
                       GODEBUG='asyncpreemptoff=1')
            env['PATH'] = str(installation / 'bin') + os.pathsep + os.environ.get('PATH', '')
            # Block HTTP(S) downloads during switch/removal, including inherited
            # lowercase proxy settings and NO_PROXY bypasses.
            offline = dict(env, HTTP_PROXY='http://127.0.0.1:1', HTTPS_PROXY='http://127.0.0.1:1',
                           http_proxy='http://127.0.0.1:1', https_proxy='http://127.0.0.1:1',
                           ALL_PROXY='http://127.0.0.1:1', all_proxy='http://127.0.0.1:1',
                           NO_PROXY='', no_proxy='')

            def record(output):
                log.write(output)
                log.flush()
                print(output, end='', flush=True)

            def run(program, *arguments, environment=None, timeout=1800):
                command = [str(program), *map(str, arguments)]
                commands.append(command)
                record('$ ' + shlex.join(command) + '\n')
                process = subprocess.Popen(command, env=env if environment is None else environment,
                                           cwd=root, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                           stderr=subprocess.STDOUT, text=True, errors='replace',
                                           start_new_session=True)
                try:
                    output, _ = process.communicate(timeout=timeout)
                except BaseException as error:
                    # Let Go cancel its transaction before forcing the entire
                    # startup-check process group to exit. Always retain output.
                    try:
                        os.killpg(process.pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                    try:
                        output, _ = process.communicate(timeout=10)
                    except subprocess.TimeoutExpired:
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        output, _ = process.communicate()
                    record(output)
                    if isinstance(error, subprocess.TimeoutExpired):
                        raise RuntimeError(f'{Path(command[0]).name} timed out after {timeout}s') from error
                    raise
                record(output)
                if process.returncode:
                    raise RuntimeError(f'{Path(command[0]).name} exited {process.returncode}')
                return output

            def check_index():
                metadata = json.loads((installation / 'index/http.json').read_text())
                index = json.loads((installation / 'index/index.json').read_text())
                assert metadata['url'] == INDEX_URL
                assert index['schema_version'] == 6
                return next(package for package in index['packages'] if package['name'] == 'oheco')

            def check_version(program, version, **options):
                assert run(program, '--version', **options).startswith(f'oo {version} ')

            old = installation / 'bin/oo@0.12.0'
            current = installation / 'bin/oo'
            versioned = installation / 'bin/oo@0.13.0'
            state_file = installation / 'state/installed.json'
            try:
                check_version(candidate, '0.13.0')
                run(candidate, 'update')
                check_index()
                run(candidate, 'install', 'oheco@0.12.0', '-y')
                check_version(old, '0.12.0')
                check_version(current, '0.12.0')
                run(old, 'update')
                package = check_index()
                assert package['latest']['ohos-arm64'] == '0.13.0', 'publish Release and Pages before running'
                run(old, 'install', 'oheco@0.13.0', '-y')
                check_version(current, '0.13.0')
                check_version(versioned, '0.13.0')
                assert current.is_symlink() and current.readlink() == Path('oo@0.13.0')
                assert versioned.is_symlink() and versioned.readlink() == Path('../packages/oheco/0.13.0/bin/oo')
                assert '--install' in run(current, 'help')
                run(current, 'update')
                check_index()
                # Both project archives come from the official catalog and GitHub;
                # no local index or artifact server is used in this verification.
                for package in ('godot-editor', 'blender'):
                    destination = root / f'{package} project with spaces'
                    assert not destination.exists()
                    run(current, 'export', package, 'app', '-o', destination)
                    assert (destination / 'build-profile.json5').is_file()
                    assert (destination / 'AppScope/app.json5').is_file()
                    marker = destination / '.oo-export-install.json'
                    assert not os.path.lexists(marker)
                    run(current, 'export', package, 'app', '-o', destination, '--install', '--dry-run', '--yes')
                    assert not os.path.lexists(marker)
                    assert not os.path.lexists(destination / '.oo-export-install.lock')
                    dry_destination = root / f'{package} dry run with spaces'
                    run(current, 'export', package, 'app', '-o', dry_destination, '--install', '--dry-run', '--yes')
                    assert not os.path.lexists(dry_destination)
                run(current, 'switch', 'oheco', '0.12.0', environment=offline, timeout=60)
                check_version(current, '0.12.0', environment=offline)
                run(current, 'switch', 'oheco', '0.13.0', environment=offline, timeout=60)
                check_version(current, '0.13.0', environment=offline)
            finally:
                # Also uninstall on a failed export or interrupted upgrade. The
                # candidate lives outside this private root and remains runnable.
                if state_file.is_file() and 'oheco' in json.loads(state_file.read_text())['packages']:
                    run(candidate, 'remove', 'oheco', '--all', '-y', environment=offline, timeout=60)
                    assert not json.loads(state_file.read_text())['packages']
                    assert all(not os.path.lexists(path) for path in (current, versioned, old))
                    assert all(not (installation / 'packages/oheco' / version).exists()
                               for version in ('0.12.0', '0.13.0'))
            result = {'status': 'passed', 'index': INDEX_URL,
                      'release': 'https://github.com/oheco/oheco/releases/tag/v0.13.0',
                      'candidate_sha256': hashlib.sha256(Path(candidate).read_bytes()).hexdigest(),
                      'validated': ['official v6 index refresh', '0.12.0 client installs released 0.13.0',
                                    'default and versioned commands', 'official Godot and Blender source exports',
                                    'export/install dry-run without receipts or output creation',
                                    'version switching with blocked network proxies', 'isolated uninstall'],
                      'commands': commands}
            (report / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
            print('Published client/index/export verification passed.', flush=True)


if __name__ == '__main__':
    main()
