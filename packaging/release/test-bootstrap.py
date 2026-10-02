#!/usr/bin/env python3
"""Exercise first-install bootstrap with real GPG signatures and no cloud writes."""
import hashlib
import http.server
import signal
import threading
import time
from unittest import mock
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[1] / "bootstrap.py"
spec = importlib.util.spec_from_file_location("bootstrap", SOURCE)
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


class BootstrapTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workspace = tempfile.TemporaryDirectory()
        cls.home = Path(cls.workspace.name)
        os.chmod(cls.home, 0o700)
        cls.gpg("--pinentry-mode", "loopback", "--passphrase", "",
                "--quick-generate-key", "Bootstrap Test <bootstrap@example.invalid>", "ed25519", "sign", "1d")
        colons = cls.gpg("--with-colons", "--list-keys").decode()
        cls.fingerprint = next(line.split(":")[9] for line in colons.splitlines() if line.startswith("fpr:"))
        cls.key = cls.gpg("--armor", "--export", cls.fingerprint)
        cls.meta = json.dumps({"channel": "test", "version": "0.0.0", "build": 24,
                              "commit": "a"*40, "release_id": "24-"+"a"*12}).encode()
        cls.name = "hypermesh-deploy_0.0.0_linux_arm64"
        cls.payload = b"test executable payload"
        cls.sums = (f"{hashlib.sha256(cls.meta).hexdigest()}  release.json\n"
                    f"{hashlib.sha256(cls.payload).hexdigest()}  {cls.name}\n").encode()
        (cls.home / "checksums").write_bytes(cls.sums)
        cls.gpg("--pinentry-mode", "loopback", "--passphrase", "", "--output",
                str(cls.home / "signature"), "--detach-sign", str(cls.home / "checksums"))
        cls.signature = (cls.home / "signature").read_bytes()

    @classmethod
    def gpg(cls, *args):
        return subprocess.run(["gpg", "--no-options", "--homedir", str(cls.home), "--batch", *args],
                              check=True, capture_output=True, timeout=30).stdout

    @classmethod
    def tearDownClass(cls):
        subprocess.run(["gpgconf", "--homedir", str(cls.home), "--kill", "all"], capture_output=True)
        cls.workspace.cleanup()

    def test_download_verification_and_no_overwrite(self):
        for mode in ("valid", "tamper", "signature", "fingerprint", "rollback", "channel", "existing", "release-substitution"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "deploy"
                if mode == "existing":
                    output.write_bytes(b"preserve")
                files = {"checksums.txt": self.sums, "checksums.txt.sig": self.signature,
                         "release.json": self.meta, self.name: self.payload}
                if mode == "tamper": files[self.name] = b"corrupt"
                if mode == "signature": files["checksums.txt.sig"] = b"invalid"
                def fetch(address, limit):
                    return files[address.rsplit("/", 1)[1]]
                def run():
                    return bootstrap.bootstrap("https://example.invalid/test/" + ("25-" if mode == "release-substitution" else "24-") + "a"*12, "prod" if mode == "channel" else "test",
                                               25 if mode == "rollback" else 24, "arm64", self.key,
                                               "0"*40 if mode == "fingerprint" else self.fingerprint, output, fetch)
                if mode == "valid":
                    run()
                    self.assertEqual(output.read_bytes(), self.payload)
                    self.assertEqual(output.stat().st_mode & 0o777, 0o700)
                else:
                    with self.assertRaises((bootstrap.Rejected, FileExistsError)):
                        run()
                    if mode == "existing": self.assertEqual(output.read_bytes(), b"preserve")
                    else: self.assertFalse(output.exists())
                self.assertFalse(list(Path(directory).glob(".hypermesh-bootstrap-*")))

    def test_bundled_trust_matches_reviewed_primary_fingerprint(self):
        self.assertEqual(bootstrap.TRUSTED_KEY, (SOURCE.parent / "trust/hypermesh-release.pub").read_bytes())
        key_path = self.home / "bundled.pub"
        key_path.write_bytes(bootstrap.TRUSTED_KEY)
        rows = self.gpg("--with-colons", "--show-keys", str(key_path)).decode().splitlines()
        primary = next(row.split(":")[9] for row in rows if row.startswith("fpr:"))
        self.assertEqual(primary, bootstrap.TRUSTED_FINGERPRINT)

    def test_cli_uses_bundled_trust_and_exports_without_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            key = Path(directory) / "release.pub"
            argv = ["bootstrap", "--release-url", "https://example.invalid/release",
                    "--channel", "test", "--min-build", "24", "--arch", "arm64",
                    "--output", str(Path(directory) / "deploy"), "--key-output", str(key)]
            with mock.patch("sys.argv", argv), mock.patch.object(bootstrap, "bootstrap", return_value=json.loads(self.meta)) as run:
                bootstrap.main()
                self.assertEqual(run.call_args.args[4:6], (bootstrap.TRUSTED_KEY, bootstrap.TRUSTED_FINGERPRINT))
                self.assertEqual(key.read_bytes(), bootstrap.TRUSTED_KEY)
                with self.assertRaises(SystemExit):
                    bootstrap.main()
                self.assertEqual(key.read_bytes(), bootstrap.TRUSTED_KEY)

    def test_reject_url_credentials_and_plaintext(self):
        for url in ("http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid/?token=x"):
            with self.assertRaises(bootstrap.Rejected):
                bootstrap.https_url(url)


class TransferDeadlineTests(unittest.TestCase):
    def test_trickling_body_hits_total_deadline(self):
        stopped = threading.Event()
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Length", "100000")
                self.end_headers()
                try:
                    while not stopped.wait(0.02):
                        self.wfile.write(b"x")
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass
        # HTTP is sufficient to exercise urllib's body-read timeout semantics;
        # public bootstrap URLs still require HTTPS before reaching fetch.
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        old_handler = signal.getsignal(signal.SIGALRM)
        started = time.monotonic()
        try:
            with mock.patch.object(bootstrap, "TRANSFER_DEADLINE_SECONDS", 0.25):
                with self.assertRaisesRegex(bootstrap.Rejected, "end-to-end deadline"):
                    bootstrap.fetch(f"http://127.0.0.1:{server.server_port}/slow", 100000)
            self.assertLess(time.monotonic() - started, 2)
            self.assertEqual(signal.getsignal(signal.SIGALRM), old_handler)
            self.assertEqual(signal.getitimer(signal.ITIMER_REAL)[0], 0)
        finally:
            stopped.set()
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_deadline_covers_open_and_restores_after_success(self):
        class SlowOpener:
            def open(self, *_args, **_kwargs):
                time.sleep(5)
        with mock.patch.object(bootstrap.urllib.request, "build_opener", return_value=SlowOpener()):
            with mock.patch.object(bootstrap, "TRANSFER_DEADLINE_SECONDS", 0.1):
                with self.assertRaisesRegex(bootstrap.Rejected, "end-to-end deadline"):
                    bootstrap.fetch("https://example.invalid/slow-headers", 10)
        with bootstrap.transfer_deadline(1):
            pass
        self.assertEqual(signal.getitimer(signal.ITIMER_REAL)[0], 0)


if __name__ == "__main__":
    unittest.main()
