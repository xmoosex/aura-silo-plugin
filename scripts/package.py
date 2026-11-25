#!/usr/bin/env python3
"""Builds the Silo install zip for each platform in the manifest, with the binary's checksum."""
import hashlib
import json
import os
import subprocess
import sys
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def add(archive, name, data, mode):
    entry = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
    entry.create_system = 3
    entry.external_attr = mode << 16
    entry.compress_type = zipfile.ZIP_DEFLATED
    archive.writestr(entry, data)


def main():
    manifest = json.loads((ROOT / "manifest.json").read_text())
    version = manifest["version"]
    out = ROOT / "dist" / version
    out.mkdir(parents=True, exist_ok=True)
    for platform in manifest["supported_platforms"]:
        goos, goarch = platform["os"], platform["arch"]
        binary = out / f"plugin-{goos}-{goarch}"
        env = {**os.environ, "GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0", "GOWORK": "off"}
        subprocess.run(["go", "build", "-buildvcs=false", "-trimpath", "-ldflags", f"-s -w -X main.version={version}",
                        "-o", str(binary), "."], cwd=ROOT, env=env, check=True)
        data = binary.read_bytes()
        packaged = (json.dumps({**manifest, "checksum": hashlib.sha256(data).hexdigest()}, indent=2) + "\n").encode()
        name = out / f"aura-silo-plugin-{version}-{goos}-{goarch}.zip"
        with zipfile.ZipFile(name, "w") as archive:
            add(archive, "manifest.json", packaged, 0o100644)
            add(archive, "plugin", data, 0o100755)
            add(archive, "LICENSE", (ROOT / "LICENSE").read_bytes(), 0o100644)
        binary.unlink()
        print(name.relative_to(ROOT))


if __name__ == "__main__":
    sys.exit(main())
