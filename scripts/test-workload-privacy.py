#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("privacy_acceptance", Path(__file__).with_name("check-workload-privacy.py"))
privacy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(privacy)


def safe():
    return {"LogConfig": {"Type": "none"}, "Ulimits": [{"Name": "core", "Soft": 0, "Hard": 0}]}


class PrivacyTests(unittest.TestCase):
    def test_only_complete_explicit_limits_pass(self):
        self.assertEqual(privacy.privacy(safe())["core_hard"], 0)
        cases = [None, {}, {"LogConfig": {"Type": "json-file"}, "Ulimits": safe()["Ulimits"]}]
        for core in [{"Name": "core"}, {"Name": "core", "Soft": 0},
                     {"Name": "core", "Hard": 0}, {"Name": "core", "Soft": False, "Hard": 0},
                     {"Name": "core", "Soft": None, "Hard": None}]:
            cases.append({"LogConfig": {"Type": "none"}, "Ulimits": [core]})
        cases.append({"LogConfig": {"Type": "none"}, "Ulimits": safe()["Ulimits"] * 2})
        for value in cases:
            with self.subTest(value=value), self.assertRaises(privacy.Rejected):
                privacy.privacy(value)

    def test_disk_scan_boundaries_and_limits(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            needle = b"unique-synthetic-needle"
            (root / "data").write_bytes(b"x" * 65530 + needle + b"tail")
            self.assertEqual(privacy.scan_files(root, needle), 1)
            with self.assertRaises(privacy.Rejected):
                privacy.scan_files(root, needle, limit=10)
            (root / "link").symlink_to(root / "data")
            with self.assertRaises(OSError):
                privacy.scan_files(root, needle)

    def test_command_bounds_and_redaction(self):
        self.assertEqual(privacy.command([sys.executable, "-c", "print('ok')"]), (0, b"ok\n"))
        with self.assertRaises(privacy.Rejected):
            privacy.command([sys.executable, "-c", "import time;time.sleep(10)"], timeout=0.05)
        with patch.object(privacy, "LIMIT", 10), self.assertRaises(privacy.Rejected):
            privacy.command([sys.executable, "-c", "print('x'*100)"])

    def exercise(self, unsafe=False, persist=False, cleanup=True, foreign=False):
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / "containers").mkdir()
            seen = {"started": False, "removed": False, "token": None, "data": b""}

            def command(args, data=None, timeout=20):
                for part in args:
                    if part.startswith("hypermesh.privacy-check="):
                        seen["token"] = part.split("=", 1)[1]
                if "create" in args:
                    self.assertIn("never", args)
                    self.assertIn("runc", args)
                    self.assertNotIn("-v", args)
                    return 0, b"id\n"
                if "start" in args:
                    seen["started"] = True
                    seen["data"] = data
                    self.assertFalse(any(data.strip() in arg.encode() for arg in args))
                    return 0, data * 2
                if "logs" in args:
                    return 0, seen["data"] if persist else b""
                if "rm" in args:
                    seen["removed"] = True
                    return (0 if cleanup else 1), b""
                return 0, b""

            def docker_json(args):
                if "{{json .Config.Labels}}" in args:
                    return {"hypermesh.privacy-check": "another-run" if foreign else seen["token"]}
                if "info" in args:
                    return directory
                result = safe()
                if unsafe:
                    result["LogConfig"]["Type"] = "json-file"
                return result

            with patch.object(privacy, "command", command), patch.object(privacy, "docker_json", docker_json):
                result = privacy.canary("sha256:" + "a"*64)
            if seen["data"]:
                self.assertNotIn(seen["data"].decode().strip(), str(result))
            return result, seen

    def test_memory_only_payload_and_cleanup(self):
        result, seen = self.exercise()
        self.assertTrue(result["passed"])
        self.assertTrue(seen["removed"])
        self.assertFalse(result["inference_executed"])

    def test_failure_still_cleans_own_container(self):
        for args in [{"unsafe": True}, {"persist": True}, {"cleanup": False}]:
            with self.subTest(args=args):
                result, seen = self.exercise(**args)
                self.assertFalse(result["passed"])
                self.assertTrue(seen["removed"])
                if args.get("unsafe"):
                    self.assertFalse(seen["started"])

    def test_foreign_container_is_not_removed(self):
        result, seen = self.exercise(foreign=True)
        self.assertFalse(result["passed"])
        self.assertFalse(seen["removed"])

    def test_requires_immutable_cached_image_id(self):
        with self.assertRaises(privacy.Rejected):
            privacy.canary("ubuntu:latest")


if __name__ == "__main__":
    unittest.main()
