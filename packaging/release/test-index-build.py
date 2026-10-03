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



class TestVersionTests(unittest.TestCase):
    def version(self, build):
        return subprocess.run(
            ["bash", "-euc", 'source "$1"; release_version_test "$2"',
             "test", str(LAYOUT), build], capture_output=True, text=True)

    def test_patch_tracks_allocation_and_retry_is_stable(self):
        for build in ("1", "57", "58", "1000"):
            result = self.version(build)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, f"0.0.{build}\n")
            self.assertEqual(self.version(build).stdout, result.stdout)

    def test_invalid_allocations_never_become_versions(self):
        for build in ("", "0", "-1", "01", "1.2", "true", "1\n2", "1;echo nope"):
            with self.subTest(build=build):
                result = self.version(build)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_publisher_uses_allocated_build_before_signing(self):
        script = LAYOUT.with_name("publish-agent.sh").read_text()
        branch = script.split('  build=$(next_build "$test_build" "$prod_build" "$run_number")', 1)[1].split("\nfi", 1)[0]
        self.assertIn('version=$(release_version_test "$build")', branch)
        self.assertNotIn('version=0.0.0', branch)
        self.assertLess(script.index('version=$(release_version_test "$build")'),
                        script.index('build_release_binaries "$root"'))

if __name__ == "__main__":
    unittest.main()
