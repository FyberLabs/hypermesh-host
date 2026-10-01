#!/usr/bin/env python3
"""Exercise the actual publisher function without cloud writes."""
from pathlib import Path
import subprocess
import unittest

SCRIPT = Path(__file__).with_name("publish-agent.sh").read_text()
FUNCTION = "upload_new() {" + SCRIPT.split("upload_new() {", 1)[1].split("\nverify_uploaded()", 1)[0]


class UploadTests(unittest.TestCase):
    def test_lookup_states(self):
        for lookup, uploaded, expected in [(0, False, 0), (1, True, 0), (2, False, 2)]:
            with self.subTest(lookup=lookup):
                code = """
set -euo pipefail
channel=test
require_channel_write() { return 0; }
blob_exists() { return "$LOOKUP"; }
az_blob() { echo UPLOAD; }
""" + FUNCTION + '\nupload_new test/example/file /unused\n'
                import os
                result = subprocess.run(["bash", "-c", code],
                                        env=dict(os.environ, LOOKUP=str(lookup)),
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual("UPLOAD" in result.stdout, uploaded)


if __name__ == "__main__":
    unittest.main()
