#!/usr/bin/env python3
"""Exercise first-install bootstrap with real GPG signatures and no cloud writes."""
import hashlib
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
        for mode in ("valid", "tamper", "signature", "fingerprint", "rollback", "channel", "existing"):
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
                    return bootstrap.bootstrap("https://example.invalid/release", "prod" if mode == "channel" else "test",
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

    def test_reject_url_credentials_and_plaintext(self):
        for url in ("http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid/?token=x"):
            with self.assertRaises(bootstrap.Rejected):
                bootstrap.https_url(url)


if __name__ == "__main__":
    unittest.main()
