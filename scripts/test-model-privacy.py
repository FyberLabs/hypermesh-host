#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("model_privacy", Path(__file__).with_name("check-model-privacy.py"))
model = importlib.util.module_from_spec(spec)
spec.loader.exec_module(model)


class ModelPrivacyTests(unittest.TestCase):
    def test_only_local_workload_network_is_accepted(self):
        self.assertEqual(model.endpoint({"hypermesh-workload": {"IPAddress": "172.18.0.2"}}), "http://172.18.0.2:8080")
        for value in [{}, {"host": {}}, {"hypermesh-workload": {}, "other": {}},
                      {"hypermesh-workload": {"IPAddress": "1.1.1.1"}},
                      {"hypermesh-workload": {"IPAddress": "127.0.0.1"}}]:
            with self.subTest(value=value), self.assertRaises(model.privacy.Rejected):
                model.endpoint(value)

    def test_redirects_cannot_forward_prompt(self):
        with self.assertRaises(model.privacy.Rejected):
            model.NoRedirect().redirect_request(None, None, 307, "redirect", {}, "https://outside.invalid")

    def exercise(self, unsafe=False, log_leak=False, disk_leak=False, journal_leak=False, bad_completion=False, restarted=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "containers").mkdir()
            seen = {"requests": [], "marker": None, "prompt": None, "state_reads": 0}

            def docker(args):
                if "{{json .Id}}" in args:
                    return "a"*64
                if "{{json .HostConfig}}" in args:
                    return {"LogConfig": {"Type": "json-file" if unsafe else "none"}, "Ulimits": [{"Name": "core", "Soft": 0, "Hard": 0}]}
                if "{{json .State}}" in args:
                    seen["state_reads"] += 1
                    return {"Running": True, "Restarting": False, "StartedAt": "changed" if restarted and seen["state_reads"] > 1 else "same", "Pid": 42}
                if "{{json .NetworkSettings.Networks}}" in args:
                    return {"hypermesh-workload": {"IPAddress": "172.18.0.2"}}
                if "{{json .Image}}" in args:
                    return "sha256:"+"b"*64
                if "info" in args:
                    return directory
                self.fail("unexpected Docker operation")

            def request(url, payload=None):
                seen["requests"].append(url)
                if payload is None:
                    return {"data": [{"id": "fixture-model"}]}
                self.assertEqual(payload["max_tokens"], 96)
                seen["prompt"] = payload["messages"][0]["content"]
                seen["marker"] = re.search(r"hypermesh_privacy_output_[0-9a-f]+", seen["prompt"])[0]
                if disk_leak:
                    (root / "containers" / "leak").write_text(seen["marker"])
                return {"choices": [{"message": {"content": "" if bad_completion else seen["marker"]}}],
                        "usage": {"prompt_tokens": 80, "completion_tokens": 40}}

            def command(args):
                self.assertNotIn("rm", args)
                self.assertNotIn("stop", args)
                if "logs" in args and log_leak or "journalctl" in args[0] and journal_leak:
                    return 0, seen["marker"].encode()
                return 0, b""

            with patch.object(model.privacy, "docker_json", docker), patch.object(model.privacy, "command", command), patch.object(model.time, "sleep"):
                try:
                    report = model.collect("fixture", request)
                    self.assertNotIn(seen["prompt"], json.dumps(report))
                    self.assertNotIn(seen["marker"], json.dumps(report))
                    return report
                finally:
                    if unsafe:
                        self.assertFalse(seen["requests"])

    def test_actual_completion_is_required_and_report_is_redacted(self):
        result = self.exercise()
        self.assertTrue(result["passed"])
        self.assertTrue(result["inference_executed"])
        self.assertFalse(result["public_renter_or_payment_journey_executed"])
        self.assertEqual(result["container_file_matches"], {"input": 0, "output": 0})

    def test_unsafe_controls_prevent_any_request(self):
        with self.assertRaises(model.privacy.Rejected):
            self.exercise(unsafe=True)

    def test_leaks_incomplete_inference_and_restart_cannot_pass(self):
        for args in [{"log_leak": True}, {"disk_leak": True}, {"journal_leak": True},
                     {"bad_completion": True}, {"restarted": True}]:
            with self.subTest(args=args), self.assertRaises(model.privacy.Rejected):
                self.exercise(**args)


if __name__ == "__main__":
    unittest.main()
