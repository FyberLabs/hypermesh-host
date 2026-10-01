#!/usr/bin/env python3
"""Channel read must return only its build, never Azure transfer metadata."""
import os
from pathlib import Path
import subprocess
import unittest

LAYOUT = Path(__file__).with_name("layout.sh")


class IndexTests(unittest.TestCase):
    def test_channel_build_return_value(self):
        for mode, success, expected in [
            ("present", True, "42\\n43\\n"),
            ("absent", True, "\\n11\\n"),
            ("lookup_error", False, ""),
            ("download_error", False, ""),
            ("malformed", False, ""),
        ]:
            with self.subTest(mode=mode):
                code = r"""
set -euo pipefail
source "$1"
blob_exists() {
  case "$MODE" in absent) return 1 ;; lookup_error) return 2 ;; esac
  return 0
}
az_blob() {
  local file=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --file ]; then file=$2; shift; fi
    shift
  done
  echo '{"downloaded":true,"metadata":"not a build number"}'
  if [ "$MODE" = download_error ]; then return 1; fi
  if [ "$MODE" = malformed ]; then
    printf '%s' '{"build":"bad"}' > "$file"
  else
    printf '%s' '{"build":42}' > "$file"
  fi
}
value=$(read_index_build test) || exit 1
printf '%s\n' "$value"
next_build "$value" "" 10
"""
                result = subprocess.run(["bash", "-c", code, "test", str(LAYOUT)],
                                        env=dict(os.environ, MODE=mode),
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, success, result.stderr)
                self.assertEqual(result.stdout, expected.replace("\\n", "\n"))


if __name__ == "__main__":
    unittest.main()
