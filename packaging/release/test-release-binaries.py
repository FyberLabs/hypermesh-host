#!/usr/bin/env python3
"""Build the actual release binaries and validate their signed-input manifest."""
import json
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
LAYOUT = ROOT / "packaging/release/layout.sh"


class ReleaseBinaries(unittest.TestCase):
    def test_cross_compiled_payload_and_checksums(self):
        with tempfile.TemporaryDirectory() as tmp:
            dist = Path(tmp)
            (dist / "release.json").write_text(json.dumps({"build": 1}))
            subprocess.run(
                ["bash", "-euc",
                 'source "$1"; build_release_binaries "$2" "$3" 0.0.0 ""; write_release_checksums "$3" 0.0.0',
                 "test", str(LAYOUT), str(ROOT), str(dist)], check=True)
            expected = {"release.json"}
            for program in ("agent", "deploy"):
                for arch, machine in (("arm64", 183), ("amd64", 62)):
                    name = f"hypermesh-{program}_0.0.0_linux_{arch}"
                    expected.add(name)
                    header = (dist / name).read_bytes()[:20]
                    self.assertEqual(header[:4], bytes([127]) + b"ELF")
                    self.assertEqual(header[4:6], bytes([2, 1]))  # ELF64 little endian
                    self.assertEqual(struct.unpack("<H", header[18:20])[0], machine)
            manifest = (dist / "checksums.txt").read_text()
            self.assertEqual({line.split()[1] for line in manifest.splitlines()}, expected)
            subprocess.run(["sha256sum", "-c", "checksums.txt"], cwd=dist, check=True)
            # The deploy CLI is protected by the same checksums as the agent.
            with (dist / "hypermesh-deploy_0.0.0_linux_arm64").open("ab") as stream:
                stream.write(b"tampered")
            result = subprocess.run(["sha256sum", "-c", "checksums.txt"], cwd=dist,
                                    capture_output=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
