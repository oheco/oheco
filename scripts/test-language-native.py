#!/usr/bin/env python3
"""Accept native oo language installs from a terminal using official sources."""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--oo', type=Path, default=Path(__file__).resolve().parent.parent / 'build/oo')
parser.add_argument('--proxy', required=True, help='HTTP(S) or SOCKS proxy used only by oo')
parser.add_argument('--tmp-parent', type=Path, required=True, help='Writable application-private directory')
parser.add_argument('--index-url', default='https://oheco.github.io/oheco-packages/index/v3/index.json')
args = parser.parse_args()
if sys.platform != 'ohos':
    parser.error('run on the HarmonyOS host')
args.tmp_parent.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix='oo-language-', dir=args.tmp_parent) as temporary:
    root = Path(temporary)
    project = root / 'project with spaces'
    project.mkdir()
    env = dict(os.environ, OHECO_ROOT=str(root / 'oo'), OHECO_INDEX_URL=args.index_url,
               OHECO_NO_AUTO_UPDATE='1', HTTP_PROXY=args.proxy, HTTPS_PROXY=args.proxy)

    def run(*command):
        child = subprocess.Popen([str(part) for part in command], cwd=project, env=env)
        try:
            code = child.wait(timeout=120)
        except subprocess.TimeoutExpired:
            child.terminate()
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
            raise
        if code:
            raise subprocess.CalledProcessError(code, command)

    oo = args.oo.resolve()
    run(oo, 'update')
    (project / 'package.json').write_text('{"name":"oo-upstream-test","version":"1.0.0","private":true}')
    run(oo, 'npm', 'install', 'is-number@7.0.0')
    run('node', '-e', "if (!require('is-number')(42) || require('is-number')('x')) process.exit(1)")
    lock = (project / 'package-lock.json').read_text()
    assert '127.0.0.1' not in lock and '"resolved"' not in lock
    run(oo, 'npm', 'ci')
    run(oo, 'npm', 'uninstall', 'is-number')
    run('python3', '-m', 'venv', root / 'venv')
    env['OHECO_PYTHON'] = str(root / 'venv/bin/python3')
    run(oo, 'pip', 'install', 'idna==3.10')
    run(env['OHECO_PYTHON'], '-c', "import idna; assert idna.encode('例子.测试') == b'xn--fsqu00a.xn--0zwm56d'")
    run(oo, 'pip', 'uninstall', '-y', 'idna')
    print('PASS official npm/PyPI through native oo proxy, terminal, spaces, lock reuse, imports and removal')
