#!/usr/bin/env python3
"""Writes index.json, the plugin repository Silo installs and updates the plugin from, for a published release.

  python3 scripts/index.py v1.0.0

It reads manifest.json and the release's assets with the gh CLI, like Silo's community catalog does.
"""
import json
import subprocess
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REPO = "xmoosex/aura-silo-plugin"


def main():
    if len(sys.argv) != 2 or not sys.argv[1].startswith("v"):
        sys.exit("usage: index.py v<version>")
    tag = sys.argv[1]

    release = json.loads(subprocess.run(
        ["gh", "release", "view", tag, "--repo", REPO, "--json", "assets,isDraft,isPrerelease"],
        check=True, capture_output=True, text=True).stdout)
    if release["isDraft"] or release["isPrerelease"]:
        sys.exit(f"{tag} is a draft or prerelease")
    assets = {asset["name"]: asset["url"] for asset in release["assets"]}
    base = f"https://github.com/{REPO}/releases/download/{tag}/"

    # Each binary's SHA-256, from the release's checksums
    if "checksums.txt" not in assets:
        sys.exit(f"{tag} has no checksums.txt")
    with urllib.request.urlopen(base + "checksums.txt") as response:
        checksums = dict(reversed(line.split()) for line in response.read().decode().splitlines() if line.strip())

    binaries = {}
    for name in assets:
        if name.startswith("plugin-") and name != "plugin.sigstore.json":
            goos, goarch = name.removeprefix("plugin-").split("-", 1)
            if name not in checksums:
                sys.exit(f"{name} isn't in checksums.txt")
            binaries[f"{goos}/{goarch}"] = {"url": base + name, "checksum": checksums[name]}
    if not binaries:
        sys.exit(f"{tag} has no plugin binaries")

    # The catalog uses the release's checksums, not the manifest's placeholder
    manifest = json.loads((ROOT / "manifest.json").read_text())
    manifest["version"] = tag.removeprefix("v")
    manifest.pop("checksum", None)

    entry = {
        "manifest": manifest,
        "repo_url": f"https://github.com/{REPO}",
        "checksums_url": base + "checksums.txt",
        "binaries": dict(sorted(binaries.items())),
    }
    if "plugin.sigstore.json" in assets:
        entry["bundle_url"] = base + "plugin.sigstore.json"

    (ROOT / "index.json").write_text(json.dumps({"plugins": [entry]}, indent=2) + "\n")
    print(f"index.json lists {manifest['plugin_id']} {manifest['version']} for {', '.join(entry['binaries'])}")


if __name__ == "__main__":
    main()
