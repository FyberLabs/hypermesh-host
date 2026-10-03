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



class SignatureRetryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import os
        import tempfile
        import time
        cls.temp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temp.name)
        cls.home = cls.root / "gnupg"
        cls.home.mkdir(mode=0o700)
        cls.env = dict(os.environ, GNUPGHOME=str(cls.home))
        cls.sums = cls.root / "checksums.txt"
        cls.sums.write_text("abc  payload\n")
        cls.gpg("--quick-generate-key", "Retry Test <retry@example.invalid>", "ed25519", "sign", "0")
        cls.old = {}
        cls.new = {}
        for suffix in ("sig", "asc"):
            path = cls.root / ("old." + suffix)
            cls.gpg(*(["--armor"] if suffix == "asc" else []),
                    "--output", str(path), "--detach-sign", str(cls.sums))
            cls.old[suffix] = path.read_bytes()
        time.sleep(1.1)
        for suffix in ("sig", "asc"):
            path = cls.root / ("new." + suffix)
            cls.gpg(*(["--armor"] if suffix == "asc" else []),
                    "--output", str(path), "--detach-sign", str(cls.sums))
            cls.new[suffix] = path.read_bytes()
            assert cls.old[suffix] != cls.new[suffix]

    @classmethod
    def gpg(cls, *args):
        subprocess.run(["gpg", "--batch", "--pinentry-mode", "loopback",
                        "--passphrase", "", *args], env=cls.env,
                       capture_output=True, check=True)

    @classmethod
    def tearDownClass(cls):
        subprocess.run(["gpgconf", "--kill", "gpg-agent"], env=cls.env,
                       capture_output=True)
        cls.temp.cleanup()

    def run_retry(self, mode):
        import tempfile
        with tempfile.TemporaryDirectory(dir=self.root) as directory:
            dist = Path(directory)
            (dist / "checksums.txt").write_bytes(
                b"different payload\n" if mode == "changed" else self.sums.read_bytes())
            for suffix in ("sig", "asc"):
                (dist / ("checksums.txt." + suffix)).write_bytes(self.new[suffix])
            code = r'''
set -euo pipefail
source "$LAYOUT"
blob_exists() {
  [ "$MODE" != lookup_error ] || return 2
  [ "$MODE" != absent ] || return 1
  if [ "$MODE" = partial ] && [[ "$1" = *.asc ]]; then return 1; fi
  return 0
}
az_blob() {
  [ "$MODE" != download_error ] || return 1
  local name="" file=""
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --name) name=$2; shift 2 ;;
      --file) file=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  if [ "$MODE" = corrupt ]; then
    printf 'invalid signature' > "$file"
  else
    cp "$FIXTURES/old.${name##*.}" "$file"
  fi
}
reuse_test_signatures 39-example "$DIST"
'''
            env = dict(self.env, LAYOUT=str(Path(__file__).with_name("layout.sh").resolve()),
                       FIXTURES=str(self.root), DIST=str(dist), MODE=mode)
            result = subprocess.run(["bash", "-c", code], env=env,
                                    capture_output=True, text=True)
            return result, {s: (dist / ("checksums.txt." + s)).read_bytes()
                            for s in ("sig", "asc")}

    def test_reuses_original_timestamped_signatures(self):
        result, signatures = self.run_retry("existing")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(signatures, self.old)

    def test_first_publish_and_partial_upload(self):
        result, signatures = self.run_retry("absent")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(signatures, self.new)
        result, signatures = self.run_retry("partial")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(signatures["sig"], self.old["sig"])
        self.assertEqual(signatures["asc"], self.new["asc"])

    def test_rejects_changed_input_bad_signature_and_storage_errors(self):
        for mode in ("changed", "corrupt", "lookup_error", "download_error"):
            with self.subTest(mode=mode):
                result, _ = self.run_retry(mode)
                self.assertNotEqual(result.returncode, 0, result.stderr)



class SupersededTagTests(unittest.TestCase):
    def run_tagging(self, mode):
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            names = Path(directory) / "names"
            names.write_text("".join(
                f"test/{n}-aaaaaaaaaaaa/hypermesh-deploy_0.0.0_linux_arm64\n"
                for n in range(1, 24)))
            code = r"""
set -euo pipefail
source "$LAYOUT"
az_blob() {
  case "$1" in
    list) cat "$NAMES" ;;
    tag)
      printf 'TAG %s\n' "$4"
      if [ "$MODE" != success ] && [[ "$4" == test/1-* ]]; then
        echo 'ERROR: fixture tagging failure' >&2
        return 1
      fi
      ;;
    exists)
      printf 'LOOKUP %s\n' "$3" >&2
      case "$MODE" in
        absent) echo false ;;
        present) echo true ;;
        lookup_error) return 1 ;;
        malformed) echo unknown ;;
        *) return 1 ;;
      esac
      ;;
    *) return 1 ;;
  esac
}
tag_superseded_test 23-aaaaaaaaaaaa 2-aaaaaaaaaaaa
echo POINTER_ALLOWED
"""
            return subprocess.run(
                ["bash", "-c", code],
                env=dict(os.environ, MODE=mode, NAMES=str(names),
                         LAYOUT=str(Path(__file__).with_name("layout.sh").resolve())),
                capture_output=True, text=True)

    def test_cleanup_race_skips_only_confirmed_missing_blob(self):
        result = self.run_tagging("absent")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("already removed", result.stderr)
        self.assertIn("TAG test/3-", result.stdout)
        self.assertIn("POINTER_ALLOWED", result.stdout)

    def test_existing_or_unconfirmed_blob_failure_blocks_pointer(self):
        for mode in ("present", "lookup_error", "malformed"):
            with self.subTest(mode=mode):
                result = self.run_tagging(mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("POINTER_ALLOWED", result.stdout)
                self.assertNotIn("already removed", result.stderr)

    def test_retention_and_current_pointer_stay_protected(self):
        result = self.run_tagging("success")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [
            "TAG test/1-aaaaaaaaaaaa/hypermesh-deploy_0.0.0_linux_arm64",
            "TAG test/3-aaaaaaaaaaaa/hypermesh-deploy_0.0.0_linux_arm64",
            "POINTER_ALLOWED",
        ])
        self.assertNotIn("LOOKUP", result.stderr)

if __name__ == "__main__":
    unittest.main()
