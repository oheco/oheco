#!/usr/bin/env python3
"""Copy descriptors into ignored loopback fixtures; never modify release metadata."""
import json
from pathlib import Path

root = Path(__file__).resolve().parent.parent.parent / "oheco-packages"
out = root / "tmp/e2e-packages"
out.mkdir(parents=True, exist_ok=True)
for name in ("oheco", "go"):
    package = json.loads((root / f"packages/{name}.json").read_text())
    for version in package["versions"]:
        for artifact in version["artifacts"].values():
            artifact["url"] = f"http://127.0.0.1:18808/artifacts/{name}.tar.gz"
    (out / f"{name}.json").write_text(json.dumps(package, ensure_ascii=False, indent=2) + "\n")
print(f"Prepared {out}")
