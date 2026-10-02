#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("acceptance", Path(__file__).with_name("host-acceptance.py"))
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


class AcceptanceTests(unittest.TestCase):
    def good(self):
        return {"schema": 1, "plan_sha256": "a"*64, "attention_required": False,
                "activation_allowed": False, "selling_allowed": False, "plane_sell_state": "paused",
                "heartbeat_age_seconds": 20, "certification_history": {"collection": "collected", "state": "active"},
                "gates": [{"id": name, "state": "passed"} for name in (
                    "installed_files", "identity", "certification_history", "activation_marker", "reboot",
                    "control_plane", "class", "intake", "certification", "schedule", "heartbeat",
                    "selling_state", "agent_service", "aiuditor")]}

    def test_fail_closed_and_redacted(self):
        good = self.good()
        self.assertFalse(acceptance.assess(good, "a"*64)["selling_enabled_by_acceptance"])
        for change in ({"attention_required": True}, {"plane_sell_state": "selling"}, {"gates": []},
                       {"heartbeat_age_seconds": 120}, {"heartbeat_age_seconds": True},
                       {"selling_allowed": True}, {"plan_sha256": "b"*64}):
            with self.assertRaises(ValueError):
                acceptance.assess({**good, **change}, "a"*64)

    def test_bounded_command_deadline_and_output(self):
        self.assertEqual(acceptance.run_json([sys.executable, "-c", "print('{}')"]), {})
        with self.assertRaises(ValueError):
            acceptance.run_json([sys.executable, "-c", "import time; time.sleep(10)"], timeout=0.1)
        with self.assertRaises(ValueError):
            acceptance.run_json([sys.executable, "-c", "print('x' * 3000000)"])
        with self.assertRaises(ValueError):
            acceptance.run_json([sys.executable, "-c", "raise SystemExit(1)"])

    def test_private_publication_no_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            acceptance.publish(path, {"ok": True})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(acceptance.read_private_json(path), {"ok": True})
            with self.assertRaises(FileExistsError):
                acceptance.publish(path, {"ok": False})
            link = Path(directory) / "link"
            link.symlink_to(path)
            with self.assertRaises(OSError):
                acceptance.read_private_json(link)


if __name__ == "__main__":
    unittest.main()
