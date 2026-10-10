"""Portable tests for the product-owned AGX soak preflight; no lab claims."""
import importlib.util
import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('agx_preflight', Path(__file__).with_name('agx-preflight.py'))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


def ready():
    return dict(memory_bytes=64 << 30, arch=preflight.ARCH, kernel='6.8.12-tegra', l4t=preflight.L4T,
                ubuntu=preflight.UBUNTU, cuda=preflight.CUDA,
                default_target=preflight.DEFAULT_TARGET,
                packages={name: True for name in preflight.INSTALLED_PACKAGES},
                boot_control=True, nvpmodel_id=preflight.NVPMODEL_ID,
                power_mode=preflight.POWER_MODE, jetson_clocks_present=True,
                jetson_clocks=False, tegrastats=True, docker=True,
                nvidia_runtime=True, wireguard_tools=True, llama_bench=False)


class PreflightTests(unittest.TestCase):
    def test_cli_pins_stay_in_sync(self):
        source = (Path(__file__).resolve().parents[2] / 'internal/deploy/preflight.go').read_text()
        for name, value in dict(pinArch=preflight.ARCH, pinL4T=preflight.L4T,
                                pinKernel=preflight.KERNEL_PREFIX, pinUbuntu=preflight.UBUNTU,
                                pinCUDA=preflight.CUDA, pinTarget=preflight.DEFAULT_TARGET,
                                pinNvpmodelWatts=preflight.POWER_MODE).items():
            self.assertEqual(re.search(r'\b' + name + r'\s*=\s*"([^"]+)"', source)[1], value)
        self.assertEqual(int(re.search(r'pinNvpmodelID\s*=\s*(\d+)', source)[1]), preflight.NVPMODEL_ID)

    def test_ready_and_each_missing_required_fact(self):
        self.assertTrue(preflight.evaluate(ready())['ok'])
        for field in ready():
            if field == 'llama_bench':
                continue  # OCI soak uses the configured NVIDIA runtime.
            with self.subTest(field=field):
                facts = ready()
                facts.pop(field)
                self.assertFalse(preflight.evaluate(facts)['ok'])
        for cuda in [preflight.CUDA_RESCUE, '13.2', '13.2.10']:
            self.assertFalse(preflight.evaluate(ready() | {'cuda': cuda})['ok'])
        self.assertFalse(preflight.evaluate(ready() | {'jetson_clocks': True})['ok'])
        self.assertFalse(preflight.evaluate(ready() | {'kernel': '6.80'})['ok'])

    def test_cuda_patch_and_fallback(self):
        self.assertIsNone(preflight.parse_cuda_text('release 13.2, V13.2.78', None, None))
        self.assertEqual(preflight.parse_cuda_text('release 13.2, V13.2.78', '{"cuda":{"version":"13.2.1"}}', None), '13.2.1')
        self.assertEqual(preflight.parse_cuda_text('release 13.2, V13.2.78', None, 'CUDA Version 13.2.1'), '13.2.1')
        self.assertEqual(preflight.parse_cuda_text('release 13.2', '{"cuda":{"version":"13.2.1"}}', None), '13.2.1')
        self.assertIsNone(preflight.parse_cuda_text('release 13.2', '{bad', None))

    def test_runtime_binary_does_not_prove_configuration(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            runtime = Path(tmp) / 'nvidia-container-runtime'
            runtime.write_text('#!/bin/sh\n')
            runtime.chmod(0o755)
            alias = Path(tmp) / 'alias'
            alias.symlink_to(runtime)
            def which(name):
                if name == 'nvidia-container-runtime':
                    return str(runtime)
                return preflight._which(name)
            def check(entry, binary=str(runtime)):
                return preflight.nvidia_runtime_configured(json.dumps({'runtimes': {'nvidia': entry}}), binary, which)
            for path in [str(runtime), str(alias), 'nvidia-container-runtime']:
                self.assertTrue(check({'path': path}))
            for entry in [None, {}, {'path': ''}, {'path': 'runc'}, {'path': '/stale/nvidia-container-runtime'}, {'path': '/bin/sh'}]:
                self.assertFalse(check(entry))
            self.assertFalse(check({'path': str(runtime)}, None))
            self.assertFalse(preflight.nvidia_runtime_configured(None, str(runtime), which))

    def test_memory_range(self):
        for memory in [32 << 30, 16 << 30, 0, None, '68719476736', (56 << 30) - 1, (66 << 30) + 1]:
            self.assertFalse(preflight.evaluate(ready() | {'memory_bytes': memory})['ok'])
        for memory in [56 << 30, 64 << 30, 66 << 30]:
            self.assertTrue(preflight.evaluate(ready() | {'memory_bytes': memory})['ok'])

    def test_failed_commands_do_not_supply_evidence(self):
        def run(cmd):
            if cmd[0] == 'nvpmodel':
                return 1, 'NV Power Mode: 30W\n2', ''
            if cmd[0] == 'jetson_clocks':
                return 1, 'not running', ''
            return 1, '', ''
        facts = preflight.collect_facts(run=run, read_text=lambda _: None,
                                        which=lambda name: '/bin/' + name,
                                        uname=lambda: ('aarch64', '6.8'))
        self.assertIsNone(facts['nvpmodel_id'])
        self.assertIsNone(facts['jetson_clocks'])
        self.assertFalse(preflight.evaluate(facts)['ok'])

    def test_clock_controls(self):
        def reader(changes):
            def read(path):
                if path in changes:
                    return changes[path]
                if path.endswith('/online'):
                    return '0-1,3'
                if path.endswith('min_freq'):
                    return '100'
                if path.endswith('max_freq'):
                    return '1000'
                return '0'
            return read
        self.assertIs(preflight.clock_lock_state(reader({})), False)
        for path in ['/sys/devices/system/cpu/cpu1/cpufreq/scaling_min_freq',
                     '/sys/devices/platform/17000000.gpu/devfreq/17000000.gpu/min_freq']:
            self.assertIs(preflight.clock_lock_state(reader({path: '1000'})), True)
            self.assertIsNone(preflight.clock_lock_state(reader({path: None})))
        for path in ['/sys/kernel/debug/bpmp/debug/clk/emc/mrq_rate_locked',
                     '/sys/kernel/debug/bpmp/debug/bwmgr/bwmgr_halt']:
            self.assertIs(preflight.clock_lock_state(reader({path: '1'})), True)
            self.assertIsNone(preflight.clock_lock_state(reader({path: '2'})))
        self.assertIsNone(preflight.clock_lock_state(reader({'/sys/devices/system/cpu/online': 'bad'})))

    def test_cli_exit_codes(self):
        with patch.object(preflight, 'collect_facts', return_value=ready()), patch('sys.stdout'), patch('sys.stderr'):
            self.assertEqual(preflight.main([]), 0)
        with patch.object(preflight, 'collect_facts', return_value={}), patch('sys.stdout'), patch('sys.stderr'):
            self.assertEqual(preflight.main([]), 3)


if __name__ == '__main__':
    unittest.main()
