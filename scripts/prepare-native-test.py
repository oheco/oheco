#!/usr/bin/env python3
"""Copy descriptors into ignored loopback fixtures; never modify release metadata."""
import json
import hashlib
from pathlib import Path
from urllib.parse import urlparse

root = Path(__file__).resolve().parent.parent.parent / "oheco-packages"
out = root / "tmp/e2e-packages"
out.mkdir(parents=True, exist_ok=True)
for obsolete in out.glob("*.json"):
    obsolete.unlink()
for name in ("oheco",):
    package = json.loads((root / f"packages/{name}.json").read_text())
    # Test the locally built release before publishing its immutable artifact.
    release = "0.5.0"
    archive = root.parent / "oheco" / "dist" / f"oheco-{release}-ohos-arm64.tar.gz"
    if not any(v["version"] == release for v in package["versions"]):
        content = archive.read_bytes()
        package["versions"].append({"version": release, "artifacts": {"ohos-arm64": {
            "url": f"https://github.com/oheco/oheco/releases/download/v{release}/{archive.name}",
            "sha256": hashlib.sha256(content).hexdigest(), "size": len(content),
            "format": "tar.gz", "strip_components": 0, "binaries": {"oo": "bin/oo"}
        }}})
    package["latest"]["ohos-arm64"] = release
    for version in package["versions"]:
        for artifact in version["artifacts"].values():
            filename = Path(urlparse(artifact["url"]).path).name
            artifact["url"] = f"http://127.0.0.1:18808/artifacts/{filename}"
    (out / f"{name}.json").write_text(json.dumps(package, ensure_ascii=False, indent=2) + "\n")
print(f"Prepared {out}")
