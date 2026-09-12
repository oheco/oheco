#!/usr/bin/env python3
"""Verify a real DevEco project export on OHOS, using an isolated package root."""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import zipfile


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--oo', required=True, type=Path)
    parser.add_argument('--log', required=True, type=Path)
    parser.add_argument('--tmp-parent', type=Path, default=Path('/data/storage/el2/base/cache'))
    parser.add_argument('--proxy', default='socks5://172.16.105.2:10808')
    parser.add_argument('--local-index', type=Path, help='Optional local candidate index served on loopback')
    parser.add_argument('--cache', type=Path, help='Reuse/save previously downloaded archive bytes; always checked by oo')
    parser.add_argument('--upgrade', action='store_true', help='Verify published 0.5.0 to 0.6.0 upgrade first')
    args = parser.parse_args()
    if sys.platform != 'ohos':
        parser.error('Run on native OpenHarmony')
    binary = args.oo.resolve()
    args.log.parent.mkdir(parents=True, exist_ok=True)
    server = None
    env = dict(os.environ, OHECO_NO_AUTO_UPDATE='1', HTTPS_PROXY=args.proxy, HTTP_PROXY=args.proxy)
    env.pop('OHECO_INDEX_URL', None)
    if args.local_index:
        data = args.local_index.read_bytes()

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_):
                pass

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        env['OHECO_INDEX_URL'] = f'http://127.0.0.1:{server.server_port}/index.json'
    try:
        with tempfile.TemporaryDirectory(prefix='oo project acceptance ', dir=args.tmp_parent) as temporary, args.log.open('w') as log:
            root = Path(temporary)
            install = root / 'package root'
            env['OHECO_ROOT'] = str(install)

            def run(*arguments, cwd=root, expected=0, environment=env, executable=binary):
                command = [str(executable), *arguments]
                log.write('+ ' + ' '.join(command) + '\n'); log.flush()
                result = subprocess.run(command, cwd=cwd, env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=1200)
                log.write(result.stdout); log.flush()
                if expected == 0 and result.returncode != 0:
                    raise RuntimeError(f'{arguments} failed: {result.returncode}')
                if expected != 0 and result.returncode == 0:
                    raise RuntimeError(f'{arguments} unexpectedly succeeded')
                return result.stdout

            run('update')
            if args.upgrade:
                if args.local_index:
                    raise RuntimeError('Upgrade acceptance requires the official published index')
                run('install', 'oheco@0.5.0')
                old = install / 'bin/oo@0.5.0'
                assert 'oo 0.5.0' in run('--version', executable=old)
                run('update', executable=old)
                run('install', 'oheco', executable=old)
                binary = install / 'bin/oo'
                assert 'oo 0.6.0' in run('--version', executable=binary)
                run('update', executable=binary)
            index = json.loads((install / 'index/index.json').read_text())
            assert index['schema_version'] == 4
            package = next(p for p in index['packages'] if p['name'] == 'godot-editor')
            version = package['latest']['ohos-arm64']
            release = next(v for v in package['versions'] if v['version'] == version)
            project = release['projects']['editor']
            cache_file = install / 'cache/downloads' / (project['sha256'] + '.' + project['format'])
            if args.cache:
                saved = args.cache / cache_file.name
                if saved.is_file():
                    shutil.copyfile(saved, cache_file)
                    log.write('Using archive bytes retained from prior native download; oo rechecks size/hash.\n')
            assert 'projects' in run('search', 'godot-editor', executable=binary)
            assert 'oo export godot-editor@' in run('info', 'godot-editor', executable=binary)
            before = (install / 'state/installed.json').read_bytes() if (install / 'state/installed.json').exists() else None
            output = root / 'Godot editor 项目'
            output.mkdir()
            (output / 'my-notes.txt').write_text('keep my notes\n')
            run('export', 'godot-editor', cwd=output, executable=binary)
            count = 0
            with zipfile.ZipFile(cache_file) as archive:
                for entry in archive.infolist():
                    if entry.is_dir():
                        continue
                    relative = Path(*Path(entry.filename).parts[project['strip_components']:])
                    with archive.open(entry) as stream:
                        expected = hashlib.file_digest(stream, 'sha256').hexdigest()
                    assert digest(output / relative) == expected, relative
                    count += 1
            assert (output / 'my-notes.txt').read_text() == 'keep my notes\n'
            assert not json.loads((output / 'build-profile.json5').read_text())['app']['signingConfigs']
            after = (install / 'state/installed.json').read_bytes() if (install / 'state/installed.json').exists() else None
            assert before == after, 'Export changed installation state'
            (output / 'build-profile.json5').write_text('my edited project\n')
            run('export', 'godot-editor', '--output', str(output), expected=1, executable=binary)
            assert (output / 'build-profile.json5').read_text() == 'my edited project\n'
            offline = dict(env, HTTPS_PROXY='http://127.0.0.1:1', HTTP_PROXY='http://127.0.0.1:1', NO_PROXY='')
            relocated = root / 'second project'
            run('export', 'godot-editor@' + version, 'editor', '-o', str(relocated), environment=offline, executable=binary)
            assert digest(relocated / 'entry/libs/arm64-v8a/libgodot.so') == digest(output / 'entry/libs/arm64-v8a/libgodot.so')
            run('install', 'godot-editor', expected=1, executable=binary)
            if args.cache:
                args.cache.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(cache_file, args.cache / cache_file.name)
            if args.upgrade:
                run('remove', 'oheco', '--all', executable=args.oo.resolve())
                assert not (install / 'bin/oo').exists()
            record = {'platform': sys.platform, 'index': env.get('OHECO_INDEX_URL', 'official v4'),
                      'package': 'godot-editor', 'version': version, 'sha256': project['sha256'],
                      'verified_exported_files': count, 'upgrade_0_5_to_0_6': args.upgrade,
                      'passed': ['download/cache validation', 'default directory', 'Chinese/space path', 'all file hashes',
                                 'installation state unchanged', 'user edit preserved', 'offline named export', 'project-only install hint']}
            args.log.with_suffix('.json').write_text(json.dumps(record, indent=2) + '\n')
            log.write('NATIVE PROJECT EXPORT ACCEPTANCE PASSED\n')
            print(json.dumps(record, indent=2))
    finally:
        if server:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    main()
