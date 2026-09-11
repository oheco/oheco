#!/usr/bin/env python3
"""Package a built and signed oo without changing its executable bytes."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser()
parser.add_argument("--version", default="0.1.0")
parser.add_argument("--binary", type=Path, default=root / "build/oo")
parser.add_argument("--output", type=Path, default=root / "dist")
args = parser.parse_args()
if not args.version or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-" for c in args.version):
    parser.error("invalid version")
args.output.mkdir(parents=True, exist_ok=True)
destination = args.output / f"oheco-{args.version}-ohos-arm64.tar.gz"
if destination.exists():
    raise SystemExit(f"Refusing to overwrite {destination}; remove a local development build explicitly or publish a new version.")
inputs = {"bin/oo": args.binary, "README.md": root / "README.md", "LICENSE": root / "LICENSE"}
with tarfile.open(destination, "w:gz", format=tarfile.PAX_FORMAT) as archive:
    for name, filename in inputs.items():
        content = filename.read_bytes()
        info = tarfile.TarInfo(name)
        info.mode = 0o755 if name == "bin/oo" else 0o644
        info.size = len(content)
        info.mtime = 0
        archive.addfile(info, io.BytesIO(content))
digest = hashlib.sha256(destination.read_bytes()).hexdigest()
destination.with_name(destination.name + ".sha256").write_text(f"{digest}  {destination.name}\n")
print(json.dumps({"file": str(destination), "size": destination.stat().st_size, "sha256": digest}, indent=2))
