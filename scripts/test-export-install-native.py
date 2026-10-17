#!/usr/bin/env python3
"""Exercise oo export --install with official tools and one owned test app.

Requires an already authorized developer account/team and HDC target. Never
removes a pre-existing app. The metadata/artifact origins are deliberately
separate. All created project/state/runtime directories live under TMPDIR.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import threading
import time
import zipfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--oo', required=True)
    parser.add_argument('--clt', required=True)
    parser.add_argument('--node', required=True)
    parser.add_argument('--cli', required=True)
    parser.add_argument('--device', required=True)
    parser.add_argument('--team')
    parser.add_argument('--source-package', help='Export this real catalog project as the test source instead of CLI scaffolding')
    parser.add_argument('--report-dir', required=True)
    args = parser.parse_args()
    report = Path(args.report_dir).resolve()
    report.mkdir(parents=True, exist_ok=True)
    clt = Path(args.clt).resolve()
    oo = str(Path(args.oo).resolve())
    bundle = 'org.oheco.ooexportfixture'
    env = os.environ.copy()
    env.update(COMMAND_LINE_TOOL_PATH=str(clt), DEVECO_SDK_HOME=str(clt / 'sdk'),
               HTTP_PROXY='socks5://127.0.0.1:10808', HTTPS_PROXY='socks5://127.0.0.1:10808',
               NO_PROXY='localhost,127.0.0.1,::1', OHECO_NO_AUTO_UPDATE='1', GODEBUG='asyncpreemptoff=1')
    hdc = str(clt / 'sdk/default/openharmony/toolchains/hdc')
    installed = False
    commands = []
    log = (report / 'native.log').open('w')

    def run(argv, cwd=None, timeout=1200, capture=False):
        commands.append([str(a) for a in argv])
        process = subprocess.Popen([str(a) for a in argv], cwd=cwd, env=env,
                                   stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True, errors='replace')
        expired = threading.Event()
        def interrupt():
            if process.poll() is None:
                expired.set()
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
        timer = threading.Timer(timeout, interrupt)
        timer.daemon = True
        timer.start()
        output = []
        try:
            for line in process.stdout:
                output.append(line)
                log.write(line)
                log.flush()
                if not capture:
                    print(line, end='', flush=True)
            code = process.wait()
        finally:
            timer.cancel()
            process.stdout.close()
        if expired.is_set():
            raise RuntimeError(f'{Path(argv[0]).name} timed out')
        if code:
            raise RuntimeError(f'{Path(argv[0]).name} exited {code}')
        return ''.join(output)

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            data = self.server.payload.get(self.path)
            if data is None:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        def log_message(self, *_):
            pass

    servers = []
    try:
        with tempfile.TemporaryDirectory(prefix='oo-export-install-native-', dir=os.environ['TMPDIR']) as work:
            root = Path(work)
            runtime = root / 'runtime'
            runtime.mkdir()
            (runtime / 'libc++_shared.so').symlink_to(clt / 'sdk/default/openharmony/native/llvm/lib/aarch64-linux-ohos/libc++_shared.so')
            env['LD_LIBRARY_PATH'] = str(runtime) + (':' + env['LD_LIBRARY_PATH'] if env.get('LD_LIBRARY_PATH') else '')
            before = run([hdc, '-t', args.device, 'shell', 'bm', 'dump', '-n', bundle], capture=True)
            if '"name": "' + bundle + '"' in before or '"bundleName": "' + bundle + '"' in before or ('"versionCode"' in before and bundle in before):
                raise RuntimeError('test bundle already exists; refusing to replace it')
            source = root / 'source'
            env['OHECO_ROOT'] = str(root / 'isolated-oo')
            if args.source_package:
                env['OHECO_INDEX_URL'] = 'https://oheco.org/index/v6/index.json'
                run([oo, 'update'])
                run([oo, 'export', args.source_package, 'app', '-o', source])
            else:
                run([args.node, args.cli, 'create', '--app-name', 'OoExportFixture',
                     '--project-path', source, '--bundle-name', bundle, '--api-level', '26'])
            configure = r'''
const fs=require('fs'), path=require('path');
const json5=require(process.argv[1]); const root=process.argv[2];
function update(relative, change) {const filename=path.join(root,relative);const value=json5.parse(fs.readFileSync(filename,'utf8'));change(value);fs.writeFileSync(filename,JSON.stringify(value,null,2)+'\n');}
const sdk=JSON.parse(fs.readFileSync(process.argv[4],'utf8')).data;
update('build-profile.json5', p=>{p.app.signingConfigs=[]; for(const product of p.app.products){if(process.argv[3]==='scaffold'){product.runtimeOS='HarmonyOS';product.compileSdkVersion=sdk.platformVersion;product.targetSdkVersion=sdk.platformVersion;product.compatibleSdkVersion=sdk.platformVersion;}delete product.signingConfig;}});
update('AppScope/app.json5', p=>{p.app.bundleName='org.oheco.ooexportfixture';p.app.versionCode=1;p.app.versionName='0.1.0';});
if(process.argv[3]==='scaffold') for(const file of ['oh-package.json5','entry/oh-package.json5','hvigor/hvigor-config.json5']) {update(file,p=>{p.dependencies={};if(p.devDependencies)p.devDependencies={};});}
'''
            run([args.node, '-e', configure, '--', clt / 'hvigor/hvigor-ohos-plugin/node_modules/json5', source, 'catalog' if args.source_package else 'scaffold', clt / 'sdk/default/sdk-pkg.json'])
            archive = root / 'fixture.zip'
            with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as output:
                for filename in sorted(source.rglob('*')):
                    if filename.is_file():
                        output.write(filename, Path('project') / filename.relative_to(source))
            payload = archive.read_bytes()
            artifact_server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
            artifact_server.payload = {'/fixture.zip': payload}
            servers.append(artifact_server)
            artifact_url = f'http://127.0.0.1:{artifact_server.server_port}/fixture.zip'
            package = {'schema_version': 4, 'name': 'oo-export-fixture', 'description': 'Owned native export/install validation fixture',
                       'upstream': 'https://github.com/oheco/oheco', 'repository': 'https://github.com/oheco/oheco',
                       'license': 'MIT', 'maintainers': [{'github': 'kdada'}], 'latest': {'ohos-arm64': '1.0'},
                       'versions': [{'version': '1.0', 'projects': {'app': {'url': artifact_url, 'size': len(payload),
                           'sha256': hashlib.sha256(payload).hexdigest(), 'format': 'zip', 'strip_components': 1}}}]}
            index = {'schema_version': 6, 'generated_at': '2026-10-17T00:00:00Z', 'packages': [package]}
            index_server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
            index_server.payload = {'/index.json': json.dumps(index).encode()}
            servers.append(index_server)
            for server in servers:
                threading.Thread(target=server.serve_forever, daemon=True).start()
            env['OHECO_ROOT'] = str(root / 'isolated-oo')
            env['OHECO_INDEX_URL'] = f'http://127.0.0.1:{index_server.server_port}/index.json'
            destination = root / 'exported project with spaces'
            command = [oo, 'export', 'oo-export-fixture@1.0', 'app', '-o', destination, '--install', '--device', args.device, '--yes']
            if args.team:
                command.extend(['--team', args.team])
            run([oo, 'update'])
            run(command + ['--dry-run'])
            if destination.exists():
                raise AssertionError('dry-run created export directory')
            installed = True
            run(command)
            receipt = json.loads((destination / '.oo-export-install.json').read_text())
            if receipt['status'] != 'installed' or receipt['bundle'] != bundle or receipt['version_code'] != 1:
                raise AssertionError('wrong installation receipt')
            (destination / 'user note.txt').write_text('retained across resume\n')
            run(command)
            if (destination / 'user note.txt').read_text() != 'retained across resume\n':
                raise AssertionError('resume changed user data')
            status = run([hdc, '-t', args.device, 'shell', 'bm', 'dump', '-n', bundle], capture=True)
            (report / 'installed-bundle.json').write_text(status)
            result = {'status': 'passed', 'bundle': bundle, 'device': args.device, 'receipt': receipt, 'source_package': args.source_package,
                      'archive_sha256': hashlib.sha256(payload).hexdigest(), 'archive_size': len(payload),
                      'metadata_origin': env['OHECO_INDEX_URL'], 'artifact_origin': artifact_url,
                      'candidate_sha256': hashlib.sha256(Path(oo).read_bytes()).hexdigest(),
                      'validated': ['dry-run', 'space-containing export path', 'official debug signing',
                                    'official OHPM/Hvigor project build with signed modules', 'signature and target Profile checks',
                                    'HDC replace-install', 'installed bundle/version', 'same-command resume with user note retained']}
            (report / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
            print('Native export/install validation passed.', flush=True)
    finally:
        if installed:
            cleanup_env = os.environ.copy()
            cleanup_env['HTTP_PROXY'] = cleanup_env['HTTPS_PROXY'] = 'socks5://127.0.0.1:10808'
            subprocess.run([hdc, '-t', args.device, 'uninstall', bundle], env=cleanup_env, stdout=log, stderr=subprocess.STDOUT, timeout=90)
        for server in servers:
            server.shutdown()
            server.server_close()
        log.close()


if __name__ == '__main__':
    main()
