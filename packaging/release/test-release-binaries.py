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
        with tempfile.TemporaryDirectory() as tmp, tempfile.TemporaryDirectory(dir=ROOT) as probe_dir:
            dist = Path(tmp)
            (dist / "release.json").write_text(json.dumps({"build": 1}))
            probe = Path(probe_dir) / "main.go"
            probe.write_text('package main\nimport ("encoding/json"; "os"; "github.com/FyberLabs/hypermesh-host/internal/buildinfo")\nfunc main(){json.NewEncoder(os.Stdout).Encode(map[string]any{"channel":buildinfo.CurrentChannel(),"version":buildinfo.CurrentVersion(),"build":buildinfo.CurrentBuild()})}\n')
            publisher = LAYOUT.with_name("publish-agent.sh").read_text()
            ldflags = next(line for line in publisher.splitlines() if line.startswith("ldflags="))
            subprocess.run(
                ["bash", "-euc",
                 'source "$1"; version=0.0.57; build=57; channel=test; GITHUB_SHA=' + 'a'*40 + '; ' +
                 ldflags + '; build_release_binaries "$2" "$3" "$version" "$ldflags"; write_release_checksums "$3" "$version"; go run -trimpath -ldflags "$ldflags" "$4" > "$3/buildinfo.json"',
                 "test", str(LAYOUT), str(ROOT), str(dist), str(probe)], check=True, cwd=ROOT)
            self.assertEqual(json.loads((dist / "buildinfo.json").read_text()),
                             {"channel": "test", "version": "0.0.57", "build": 57})
            expected = {"release.json", "bootstrap.py", "platforms.json"}
            for program in ("agent", "deploy"):
                for target, machine in (("linux_arm64", 183), ("linux_amd64", 62), ("darwin_arm64", 0x100000c)):
                    name = f"hypermesh-{program}_0.0.57_{target}"
                    expected.add(name)
                    header = (dist / name).read_bytes()[:20]
                    if target.startswith("darwin"):
                        self.assertEqual(header[:4], bytes.fromhex("cffaedfe"))
                        self.assertEqual(struct.unpack("<I", header[4:8])[0], machine)
                    else:
                        self.assertEqual(header[:4], bytes([127]) + b"ELF")
                        self.assertEqual(header[4:6], bytes([2, 1]))
                        self.assertEqual(struct.unpack("<H", header[18:20])[0], machine)
                    metadata = subprocess.check_output(["go", "version", "-m", str(dist / name)], text=True)
                    self.assertEqual("-tags=rocm,cpu" in metadata, target == "linux_amd64")
                    self.assertEqual("-tags=cpu" in metadata, target == "linux_arm64")
            self.assertEqual(json.loads((dist / "platforms.json").read_text()),
                             json.loads((ROOT / "internal/hardware/systems.json").read_text()))
            manifest = (dist / "checksums.txt").read_text()
            self.assertEqual({line.split()[1] for line in manifest.splitlines()}, expected)
            subprocess.run(["sha256sum", "-c", "checksums.txt"], cwd=dist, check=True)
            # The deploy CLI is protected by the same checksums as the agent.
            with (dist / "hypermesh-deploy_0.0.57_linux_arm64").open("ab") as stream:
                stream.write(b"tampered")
            result = subprocess.run(["sha256sum", "-c", "checksums.txt"], cwd=dist,
                                    capture_output=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
