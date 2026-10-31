import json
import os
import subprocess


def api(*args):
    return json.loads(subprocess.check_output(["gh", "api", *args], text=True))


def promote(repository, tag, release_id):
    prefix = f"repos/{repository}/releases"
    target = api(f"{prefix}/{release_id}")
    if target["tag_name"] != tag or target["draft"] or target["prerelease"]:
        raise ValueError("latest target is not the exact public stable release")
    current = api(f"{prefix}/latest")
    comparison = int(subprocess.check_output(
        [os.environ["RELEASE_VERSION_COMPARE"], tag.removeprefix("v"), current["tag_name"].removeprefix("v")],
        text=True,
    ))
    if comparison < 0 or current["id"] == target["id"]:
        return False
    api("--method", "PATCH", f"{prefix}/{release_id}", "-f", "make_latest=true")
    return True


if __name__ == "__main__":
    promote(os.environ["GITHUB_REPOSITORY"], os.environ["RELEASE_TAG"], os.environ["RELEASE_ID"])
