#!/usr/bin/env python3
"""Real cleanup filesystem effects, simulated CLI/logger races; no credentials."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('cleanup.sh')


class CleanupTests(unittest.TestCase):
    def run_case(self, mode, signing=True):
        with tempfile.TemporaryDirectory(prefix='release cleanup ') as directory:
            root = Path(directory)
            bindir = root / 'bin'
            bindir.mkdir()
            allowed = root / 'allowed' if mode == 'unsafe' else root
            allowed.mkdir(exist_ok=True)
            azure = allowed / 'az'
            azure.mkdir()
            (azure / 'token-cache').write_text('synthetic test secret')
            signing_dir = root / 'signing'
            signing_dir.mkdir()
            (signing_dir / 'key').write_text('synthetic signing fixture')
            log = root / 'calls'
            for name, source in {
                'az': '#!/bin/bash\necho logout >> "$CALLS"\nexit 1\n',
                'gpgconf': '#!/bin/bash\necho gpg-stop >> "$CALLS"\nexit 0\n',
                'sleep': '#!/bin/bash\nexit 0\n',
                'rm': '''#!/bin/bash
printf '%s\\n' "$*" >> "$CALLS"
target="${@: -1}"
if [ "$MODE" = persistent ] || { [ "$MODE" = signing_failure ] && [ "$target" = "$GNUPGHOME" ]; }; then exit 1; fi
/bin/rm "$@"
if [ "$target" = "$AZURE_CONFIG_DIR" ] && [ "$MODE" = race ] && [ ! -f "$RACE_SEEN" ]; then
  touch "$RACE_SEEN"
  mkdir -p "$target/logs"
  echo 'Directory not empty' >&2
  exit 1
fi
''',
            }.items():
                p = bindir / name
                p.write_text(source)
                p.chmod(0o700)
            env = dict(os.environ, PATH=str(bindir)+os.pathsep+os.environ['PATH'],
                       RUNNER_TEMP=str(allowed), TMPDIR=str(allowed),
                       GNUPGHOME=str(signing_dir) if signing else '',
                       AZURE_CONFIG_DIR='' if mode == 'azure_unset' else str(azure), MODE=mode,
                       CALLS=str(log), RACE_SEEN=str(root/'race-seen'))
            result = subprocess.run(['bash', str(SCRIPT)], env=env, capture_output=True, text=True, timeout=10)
            calls = log.read_text().splitlines()
            self.assertNotIn('synthetic', result.stdout+result.stderr)
            if mode == 'unsafe':
                self.assertEqual((signing_dir/'key').read_text(), 'synthetic signing fixture')
            return result, signing_dir.exists(), azure.exists(), calls

    def test_logger_recreates_directory_then_retry_removes_it(self):
        result, signing, azure, calls = self.run_case('race')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(signing or azure)
        self.assertEqual(sum('/az' in call for call in calls), 2)
        self.assertLess(calls.index('gpg-stop'), calls.index('logout'))

    def test_permanent_failure_is_bounded_and_fatal(self):
        result, signing, azure, calls = self.run_case('persistent')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(signing and azure)
        self.assertEqual(sum('/az' in call for call in calls), 5)
        self.assertIn('logout', calls)

    def test_signing_failure_still_removes_azure_credentials(self):
        result, signing, azure, _ = self.run_case('signing_failure')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(signing)
        self.assertFalse(azure)

    def test_unset_azure_path_does_not_log_out_default_profile(self):
        result, signing, azure, calls = self.run_case('azure_unset')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(signing)
        self.assertTrue(azure)
        self.assertNotIn('logout', calls)

    def test_unsafe_signing_path_is_never_shredded(self):
        result, signing, azure, calls = self.run_case('unsafe')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(signing)
        self.assertFalse(azure)
        self.assertNotIn('gpg-stop', calls)

    def test_cleanup_before_signing_home_was_allocated(self):
        result, _, azure, calls = self.run_case('normal', signing=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(azure)
        self.assertNotIn('gpg-stop', calls)


if __name__ == '__main__':
    unittest.main()
