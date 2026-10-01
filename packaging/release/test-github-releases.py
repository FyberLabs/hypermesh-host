#!/usr/bin/env python3
"""Exercise the publisher's GitHub stage against a persistent fake API."""
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest
from urllib.parse import urlsplit, parse_qs

HERE = Path(__file__).resolve().parent


def fake_curl(args):
    state_path = Path(os.environ["FAKE_STATE"])
    state = json.loads(state_path.read_text())
    def opt(name, default=None):
        return args[args.index(name) + 1] if name in args else default
    url = urlsplit(args[-1])
    path = url.path.split("/repos/owner/host/")[-1]
    method = opt("-X", "GET")
    state["calls"].append([method, path])
    code, body = 404, {}
    fail = state.get("fail")
    if fail == [method, path]:
        code = 503
    elif path.startswith("git/ref/tags/") and method == "GET":
        tag = path.removeprefix("git/ref/tags/")
        if tag in state["tags"]:
            code, body = 200, {"object": {"type": "commit", "sha": state["tags"][tag]}}
    elif path.startswith("git/refs/tags/") and method == "DELETE":
        tag = path.removeprefix("git/refs/tags/")
        if tag in state["tags"]:
            del state["tags"][tag]
            code = 204
    elif path.startswith("releases/tags/"):
        tag = path.removeprefix("releases/tags/")
        if tag in state["releases"]:
            code, body = 200, state["releases"][tag]
    elif path == "releases" and method == "POST":
        payload = json.loads(opt("--data"))
        tag = payload["tag_name"]
        if tag in state["releases"]:
            code = 422
        else:
            state["next"] += 1
            body = dict(payload, id=state["next"], assets=[])
            state["releases"][tag] = body
            state["tags"].setdefault(tag, payload["target_commitish"])
            code = 201
    elif path.startswith("releases/"):
        parts = path.split("/")
        rel = next((r for r in state["releases"].values() if str(r["id"]) == parts[1]), None)
        if rel and len(parts) == 2 and method == "DELETE":
            del state["releases"][rel["tag_name"]]
            code = 204
        elif rel and len(parts) == 3 and parts[2] == "assets":
            if method == "GET":
                code, body = 200, rel["assets"]
            elif method == "POST":
                name = parse_qs(url.query)["name"][0]
                data = Path(opt("--data-binary")[1:]).read_bytes()
                if any(a["name"] == name for a in rel["assets"]):
                    code = 422
                else:
                    rel["assets"].append({"name": name, "id": len(rel["assets"]) + 1,
                                          "digest": "sha256:" + hashlib.sha256(data).hexdigest()})
                    code = 201
    state_path.write_text(json.dumps(state))
    Path(opt("-o")).write_text(json.dumps(body))
    print(code, end="")


class GithubReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        self.statefile = self.root / "state.json"
        self.write({"tags": {}, "releases": {}, "calls": [], "next": 0})
        curl = self.root / "curl"
        curl.write_text("#!/bin/sh\nexec " + shlex.quote(sys.executable) + " " +
                        shlex.quote(str(Path(__file__).resolve())) + ' --fake-curl "$@"\n')
        curl.chmod(0o755)
        for name in ("release.json", "checksums.txt", "checksums.txt.sig", "agent"):
            (self.dist / name).write_text(name + ": signed bytes")

    def read(self):
        return json.loads(self.statefile.read_text())

    def write(self, state):
        self.statefile.write_text(json.dumps(state))

    def publish(self, build=123, sha="a" * 40, channel="test", ok=True):
        rid = str(build) + "-" + sha[:12] if channel == "test" else "1.2.3"
        env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ["PATH"],
                   FAKE_STATE=str(self.statefile), GH_TOKEN="test-only",
                   GITHUB_REPOSITORY="owner/host", GITHUB_SHA=sha,
                   dist=str(self.dist), channel=channel, release_id=rid,
                   tag="test" if channel == "test" else "v1.2.3",
                   prerelease="true" if channel == "test" else "false", reuse="0")
        script = (HERE / "publish-agent.sh").read_text().split('auth=(-H', 1)[1]
        code = ("set -euo pipefail\nshopt -s inherit_errexit\nsource " +
                shlex.quote(str(HERE / "layout.sh")) +
                "\nartifact_names=(release.json checksums.txt checksums.txt.sig agent)\nauth=(-H" + script)
        result = subprocess.run(["bash", "-c", code], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, ok, result.stdout + result.stderr)
        return "test-" + rid if channel == "test" else "v1.2.3"

    def test_two_builds_preserved_and_alias_advances(self):
        first = self.publish()
        old = self.read()["releases"][first]
        second = self.publish(124, "b" * 40)
        state = self.read()
        self.assertEqual(set(state["releases"]), {first, second, "test"})
        self.assertEqual(state["releases"][first], old)
        self.assertEqual(state["tags"]["test"], "b" * 40)
        for tag in (first, second, "test"):
            self.assertTrue(state["releases"][tag]["prerelease"])
            self.assertEqual(len(state["releases"][tag]["assets"]), 4)
        self.assertEqual(state["releases"][second]["assets"], state["releases"]["test"]["assets"])

    def test_existing_archive_repairs_missing_assets(self):
        tag = self.publish()
        state = self.read()
        original_id = state["releases"][tag]["id"]
        state["releases"][tag]["assets"].pop()
        self.write(state)
        self.publish()
        state = self.read()
        self.assertEqual(state["releases"][tag]["id"], original_id)
        self.assertEqual(len(state["releases"][tag]["assets"]), 4)

    def test_conflicting_bytes_leave_alias_untouched(self):
        tag = self.publish()
        state = self.read()
        alias = state["releases"]["test"]
        state["releases"][tag]["assets"][0]["digest"] = "sha256:" + "0" * 64
        self.write(state)
        self.publish(ok=False)
        self.assertEqual(self.read()["releases"]["test"], alias)

    def test_wrong_tag_target_leaves_alias_untouched(self):
        tag = self.publish()
        state = self.read()
        alias = state["releases"]["test"]
        state["tags"][tag] = "c" * 40
        self.write(state)
        self.publish(ok=False)
        self.assertEqual(self.read()["releases"]["test"], alias)

    def test_archive_upload_failure_does_not_move_alias(self):
        self.publish()
        state = self.read()
        alias = state["releases"]["test"]
        state["fail"] = ["POST", "releases/3/assets"]
        self.write(state)
        self.publish(124, "b" * 40, ok=False)
        self.assertEqual(self.read()["releases"]["test"], alias)
        state = self.read()
        del state["fail"]
        self.write(state)
        self.publish(124, "b" * 40)
        self.assertEqual(self.read()["tags"]["test"], "b" * 40)

    def test_api_errors_are_not_absence(self):
        for path in ("git/ref/tags/test-123-aaaaaaaaaaaa", "releases/tags/test-123-aaaaaaaaaaaa",
                     "releases/tags/test", "releases/2", "git/refs/tags/test"):
            with self.subTest(path=path):
                self.write({"tags": {}, "releases": {}, "calls": [], "next": 0})
                self.publish()
                state = self.read()
                method = "DELETE" if path in ("releases/2", "git/refs/tags/test") else "GET"
                state["fail"] = [method, path]
                self.write(state)
                self.publish(ok=False)

    def test_prod_reuses_version_without_creating_test_alias(self):
        tag = self.publish(channel="prod")
        old = self.read()["releases"][tag]
        self.publish(channel="prod")
        self.assertEqual(self.read()["releases"], {tag: old})
        self.assertFalse(old["prerelease"])


if __name__ == "__main__":
    if sys.argv[1:2] == ["--fake-curl"]:
        fake_curl(sys.argv[2:])
    else:
        unittest.main()
