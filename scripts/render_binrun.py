import json
import os
import subprocess
import sys

PLATFORMS = {"macos-aarch64": "darwin_arm64", "macos-x86_64": "darwin_amd64", "linux-x86_64": "linux_amd64", "linux-aarch64": "linux_arm64"}


def render(repository, tag):
    release = json.loads(subprocess.check_output(["gh", "api", f"repos/{repository}/releases/tags/{tag}"], text=True))
    assets = {asset["name"]: asset for asset in release["assets"]}
    platforms = {}
    for platform, target in PLATFORMS.items():
        asset = assets[f"cc-notes_{target}"]
        platforms[platform] = {
            "size": asset["size"],
            "hash": "sha256",
            "digest": asset["digest"].removeprefix("sha256:"),
            "path": "cc-notes",
            "providers": [{"type": "github-release", "repo": repository, "tag": tag, "name": asset["name"]}],
        }
    descriptor = {"schema": 1, "name": "cc-notes", "kind": "release-binary", "version": {"static": tag.removeprefix("v")}, "platforms": platforms}
    return "#!/usr/bin/env binrun\n" + json.dumps(descriptor, indent=2) + "\n"


if __name__ == "__main__":
    sys.stdout.write(render(os.environ["GITHUB_REPOSITORY"], os.environ["RELEASE_TAG"]))
