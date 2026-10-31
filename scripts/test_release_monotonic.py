import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest


class PluginVersionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.remote = self.root / "remote.git"
        self.seed = self.root / "seed"
        self.checkout = self.root / "checkout"
        self.manifest = Path("plugin/.claude-plugin/plugin.json")
        self.env = {
            key: value
            for key, value in os.environ.items()
            if key not in {
                "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
                "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
            }
        } | {
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.com",
            "GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.com",
        }
        self.git(self.root, "init", "--bare", "--initial-branch=main", str(self.remote))
        self.git(self.root, "clone", str(self.remote), str(self.seed))
        self.write_manifest(self.seed, "1.0.9")
        (self.seed / "unrelated.txt").write_text("initial\n")
        self.git(self.seed, "add", ".")
        self.git(self.seed, "commit", "-m", "initial")
        self.git(self.seed, "push", "origin", "main")

    def git(self, cwd, *args):
        result = subprocess.run(
            ["git", *args], cwd=cwd, env=self.env, capture_output=True,
            text=True, timeout=15,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout.strip()

    def write_manifest(self, root, version):
        path = root / self.manifest
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps({"name": "fixture", "version": version, "metadata": {"version": "preserve"}}, indent=2) + "\n")

    def advance(self, version):
        self.write_manifest(self.seed, version)
        self.git(self.seed, "add", ".")
        self.git(self.seed, "commit", "-m", "advance")
        self.git(self.seed, "push", "origin", "main")

    def head(self):
        return self.git(self.root, "--git-dir", str(self.remote), "rev-parse", "main")

    def current(self):
        return json.loads(self.git(self.root, "--git-dir", str(self.remote), "show", f"main:{self.manifest}"))

    def publish(self, version):
        result = subprocess.run(
            ["bash", os.environ["SYNC_PLUGIN_SCRIPT"]], cwd=self.checkout,
            env=self.env | {"TAG": "v" + version}, capture_output=True,
            text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.current()["metadata"], {"version": "preserve"})

    def clone_checkout(self):
        self.git(self.root, "clone", str(self.remote), str(self.checkout))

    def race(self, version):
        previous = self.head()
        self.write_manifest(self.seed, version)
        (self.seed / "unrelated.txt").write_text("concurrent\n")
        self.git(self.seed, "add", ".")
        self.git(self.seed, "commit", "-m", "concurrent")
        next_head = self.git(self.seed, "rev-parse", "HEAD")
        self.git(self.seed, "push", "origin", "HEAD:refs/heads/concurrent")
        marker = self.root / "race-fired"
        hook = self.checkout / ".git/hooks/pre-push"
        hook.write_text(
            "#!/bin/sh\nset -eu\n"
            f"if [ ! -e {shlex.quote(str(marker))} ]; then\n"
            f"  touch {shlex.quote(str(marker))}\n"
            "  unset GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE GIT_INDEX_FILE\n"
            "  unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES\n"
            f"  git --git-dir={shlex.quote(str(self.remote))} update-ref refs/heads/main {next_head} {previous}\n"
            "fi\n"
        )
        hook.chmod(0o755)
        return marker

    def test_older_and_exact_equal_do_not_mutate(self):
        self.advance("1.0.11")
        self.clone_checkout()
        before = self.head()
        self.publish("1.0.10")
        self.publish("1.0.11")
        self.assertEqual(self.head(), before)

    def test_rc_order_and_release_transition(self):
        self.advance("1.0.10-rc.2")
        self.clone_checkout()
        self.publish("1.0.10-rc.10")
        self.assertEqual(self.current()["version"], "1.0.10-rc.10")
        self.publish("1.0.10")
        self.assertEqual(self.current()["version"], "1.0.10")
        before = self.head()
        self.publish("1.0.10-rc.11")
        self.assertEqual(self.head(), before)

    def test_equal_build_precedence_keeps_original_text_update_policy(self):
        self.advance("1.0.10+previous")
        self.clone_checkout()
        self.publish("1.0.10+next")
        self.assertEqual(self.current()["version"], "1.0.10+next")

    def test_newer_rc_race_is_recompared(self):
        self.clone_checkout()
        marker = self.race("1.0.10-rc.10")
        self.publish("1.0.10-rc.2")
        self.assertTrue(marker.is_file())
        self.assertEqual(self.current()["version"], "1.0.10-rc.10")

    def test_equal_build_race_keeps_original_writer_policy(self):
        self.clone_checkout()
        marker = self.race("1.0.10+concurrent")
        self.publish("1.0.10+target")
        self.assertTrue(marker.is_file())
        self.assertEqual(self.current()["version"], "1.0.10+target")

    def test_malformed_current_fails_without_publication(self):
        self.advance("dev")
        self.clone_checkout()
        before = self.head()
        result = subprocess.run(
            ["bash", os.environ["SYNC_PLUGIN_SCRIPT"]], cwd=self.checkout,
            env=self.env | {"TAG": "v1.0.10"}, capture_output=True,
            text=True, timeout=30,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('invalid release version "dev"', result.stderr)
        self.assertEqual(self.head(), before)

    def test_older_race_reapplies_newer_and_preserves_unrelated(self):
        self.clone_checkout()
        marker = self.race("1.0.10")
        self.publish("1.0.11")
        self.assertTrue(marker.is_file())
        self.assertEqual(self.current()["version"], "1.0.11")
        self.assertEqual(self.git(self.root, "--git-dir", str(self.remote), "show", "main:unrelated.txt"), "concurrent")


class LatestReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state = self.root / "state.json"
        self.script = Path(__file__).with_name("promote_latest.py").resolve()
        gh = self.root / "gh"
        gh.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\nfrom pathlib import Path\n"
            "path = Path(os.environ['RELEASE_FIXTURE_STATE'])\n"
            "state = json.loads(path.read_text())\n"
            "args = sys.argv[1:]\nassert args.pop(0) == 'api'\n"
            "if args[0] == '--method':\n"
            "    assert args[:2] == ['--method', 'PATCH']\n"
            "    assert args[3:] == ['-f', 'make_latest=true']\n"
            "    identity = int(args[2].rsplit('/', 1)[1])\n"
            "    state['latest'] = identity\n    state['writes'].append(identity)\n"
            "    path.write_text(json.dumps(state))\n"
            "else:\n"
            "    assert len(args) == 1\n"
            "    identity = state['latest'] if args[0].endswith('/latest') else int(args[0].rsplit('/', 1)[1])\n"
            "print(json.dumps(state['releases'][str(identity)]))\n"
        )
        gh.chmod(0o755)
        self.env = os.environ | {
            "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
            "RELEASE_FIXTURE_STATE": str(self.state),
            "GITHUB_REPOSITORY": "owner/project",
        }
        self.set_state("1.0.9", "1.0.10", "1.0.11")

    def set_state(self, *versions):
        releases = {
            str(index): {"id": index, "tag_name": "v" + version, "draft": False, "prerelease": False}
            for index, version in enumerate(versions, 9)
        }
        self.state.write_text(json.dumps({"releases": releases, "latest": 9, "writes": []}))

    def run_promotion(self, identity):
        state = json.loads(self.state.read_text())
        release = state["releases"][str(identity)]
        return subprocess.run(
            ["python3", str(self.script)],
            env=self.env | {"RELEASE_TAG": release["tag_name"], "RELEASE_ID": str(identity)},
            capture_output=True, text=True, timeout=15,
        )

    def promote(self, identity):
        result = self.run_promotion(identity)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_newer_then_older_preserves_latest(self):
        self.promote(11)
        self.promote(10)
        state = json.loads(self.state.read_text())
        self.assertEqual(state["latest"], 11)
        self.assertEqual(state["writes"], [11])

    def test_older_then_newer_advances_latest(self):
        self.promote(10)
        self.promote(11)
        self.promote(11)
        state = json.loads(self.state.read_text())
        self.assertEqual(state["latest"], 11)
        self.assertEqual(state["writes"], [10, 11])

    def test_equal_build_precedence_preserves_explicit_latest_policy(self):
        self.set_state("1.0.10+previous", "1.0.10+next")
        self.promote(10)
        state = json.loads(self.state.read_text())
        self.assertEqual(state["latest"], 10)
        self.assertEqual(state["writes"], [10])

    def test_draft_or_prerelease_cannot_be_promoted(self):
        for flag in ["draft", "prerelease"]:
            with self.subTest(flag=flag):
                self.set_state("1.0.9", "1.0.10")
                state = json.loads(self.state.read_text())
                state["releases"]["10"][flag] = True
                self.state.write_text(json.dumps(state))
                result = self.run_promotion(10)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(json.loads(self.state.read_text())["writes"], [])

    def test_workflow_owns_latest_serialization(self):
        workflow = json.loads(Path(os.environ["RELEASE_WORKFLOW_JSON"]).read_text())
        jobs = workflow["jobs"]
        promotion = jobs["promote-latest"]
        self.assertEqual(promotion["concurrency"], {
            "group": "cc-notes-stable-promotion", "queue": "max", "cancel-in-progress": False,
        })
        self.assertEqual(set(promotion["needs"]), {"release", "helper-app", "bump-formula", "sync-plugin-version"})
        self.assertEqual(promotion["if"], "${{ !contains(github.ref_name, '-') }}")
        publication = next(step for step in jobs["release"]["steps"] if "publish-draft-release@" in step.get("uses", ""))
        self.assertIs(publication["with"]["make-latest"], False)


if __name__ == "__main__":
    unittest.main()
