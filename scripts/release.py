#!/usr/bin/env python3
"""Releases a new version: raises manifest.json "version", commits, tags v<version> and pushes both.

  make release VERSION=1.0.1
  make release VERSION=1.0.1 DRY_RUN=1   # shows what it would do

The release workflow then builds, signs and publishes the release, adds the zip, and updates index.json on main.
"""
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def git(*args, capture=True):
    result = subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=capture, text=True)
    return result.stdout.strip() if capture else ""


def fail(message):
    sys.exit(f"release: {message}")


def main():
    if len(sys.argv) < 2 or not sys.argv[1]:
        fail("set the version, like: make release VERSION=1.0.1")
    version = sys.argv[1].removeprefix("v")
    dry_run = len(sys.argv) > 2 and sys.argv[2] not in ("", "0")
    match = SEMVER.match(version)
    if not match:
        fail(f"'{version}' isn't a version like 1.0.1")
    tag = f"v{version}"

    # Released from an up-to-date, clean main, as the release workflow only takes tags on main
    if git("rev-parse", "--abbrev-ref", "HEAD") != "main":
        fail("switch to main first")
    if git("status", "--porcelain"):
        fail("commit or stash your changes first")
    git("fetch", "--quiet", "origin")
    if git("rev-parse", "HEAD") != git("rev-parse", "origin/main"):
        behind = git("rev-list", "--count", "HEAD..origin/main")
        ahead = git("rev-list", "--count", "origin/main..HEAD")
        if behind != "0":
            fail(f"main is {behind} commit(s) behind origin, run git pull first")
        print(f"Your {ahead} commit(s) on main are pushed with the release")
    if git("tag", "--list", tag) or git("ls-remote", "--tags", "origin", f"refs/tags/{tag}"):
        fail(f"{tag} already exists, use a higher version")

    # A version is only released once, so it must be higher
    manifest_path = ROOT / "manifest.json"
    text = manifest_path.read_text()
    current = json.loads(text)["version"]
    current_match = SEMVER.match(current)
    if current_match and tuple(map(int, match.groups())) <= tuple(map(int, current_match.groups())):
        fail(f"{version} isn't higher than the current {current}")

    if dry_run:
        print(f"Would raise manifest.json from {current} to {version}, commit, tag {tag} and push main and {tag}")
        return

    subprocess.run(["make", "check"], cwd=ROOT, check=True)

    # Only the version line changes, so the rest of manifest.json keeps its formatting
    updated, count = re.subn(r'("version":\s*")[^"]*(")', rf"\g<1>{version}\g<2>", text, count=1)
    if count != 1 or json.loads(updated)["version"] != version:
        fail("couldn't change the version in manifest.json")
    manifest_path.write_text(updated)

    git("add", "manifest.json")
    git("commit", "--quiet", "-m", f"chore: release {tag}")
    git("tag", tag)
    git("push", "origin", "main", capture=False)
    git("push", "origin", tag, capture=False)
    print(f"Released {tag}. Follow the release at https://github.com/xmoosex/aura-silo-plugin/actions")
    print("The workflow updates index.json on main afterwards, so run git pull before your next change")


if __name__ == "__main__":
    main()
