#!/usr/bin/env python3
"""Regression tests for optional passphrase loading; no Azure credentials used."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class SigningMaterialTests(unittest.TestCase):
    def run_case(self, mode, expected, passphrase=""):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bindir = root / "bin"
            bindir.mkdir()
            az = bindir / "az"
            az.write_text("""#!/bin/bash
case "$*" in
  *"--name signing-key"*)
    if [ "$MODE" = key_missing ]; then
      echo 'ERROR: (SecretNotFound) key missing' >&2
      exit 1
    fi
    echo 'fake private key'
    ;;
  *)
    case "$MODE" in
      absent) echo 'ERROR: (SecretNotFound) optional secret missing' >&2; exit 1 ;;
      denied) echo 'ERROR: (Forbidden) access denied' >&2; exit 1 ;;
      disabled) echo 'ERROR: (SecretDisabled) disabled' >&2; exit 1 ;;
      network) echo 'connection failed' >&2; exit 1 ;;
      unclassified) echo 'SecretNotFound mentioned in unrelated output' >&2; exit 1 ;;
      *) printf '%s\\n' "$TEST_PASSPHRASE" ;;
    esac
    ;;
esac
""")
            gpg = bindir / "gpg"
            gpg.write_text("""#!/bin/bash
case "$*" in
  *--import*)
    [ "$MODE" != import_failed ] || exit 1
    ;;
  *--list-secret-keys*) echo 'fpr:::::::::TESTFINGERPRINT:' ;;
esac
""")
            az.chmod(0o700)
            gpg.chmod(0o700)
            env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ["PATH"],
                       MODE=mode, TEST_PASSPHRASE=passphrase, TMPDIR=directory)
            result = subprocess.run(
                ["bash", "-c",
                 'source "$1"; load_signing_material test-vault signing-key signing-pass || exit $?; '
                 'test "$(cat "$GNUPGHOME/pass")" = "$TEST_PASSPHRASE"',
                 "test", str(ROOT / "packaging/release/layout.sh")],
                env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode == 0, expected, result.stderr)
            self.assertNotIn("fake private key", result.stdout + result.stderr)

    def test_optional_absent(self):
        self.run_case("absent", True)

    def test_present_empty(self):
        self.run_case("present", True)

    def test_present_protected(self):
        self.run_case("present", True, "sample passphrase")

    def test_failures_remain_fatal(self):
        for mode in ("denied", "disabled", "network", "unclassified", "key_missing", "import_failed"):
            with self.subTest(mode=mode):
                self.run_case(mode, False)


if __name__ == "__main__":
    unittest.main()
