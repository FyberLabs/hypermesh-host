import ctypes
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("uma_probe", Path(__file__).with_name("uma_probe.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class Function:
    def __init__(self, run):
        self.run = run

    def __call__(self, *args):
        return self.run(*args)


class Driver:
    def __init__(self, backend, integrated=True, count=1, free=48 << 30):
        self.calls = []
        self.backend, self.integrated, self.count, self.free = backend, integrated, count, free
        self.fail = ""

    def __getattr__(self, name):
        def run(*args):
            self.calls.append(name)
            if name == self.fail:
                return 1
            if name.endswith("GetDeviceCount") or name == "cuDeviceGetCount":
                args[0]._obj.value = self.count
            elif name == "cuDeviceGet":
                args[0]._obj.value = args[1]
            elif name.endswith("DeviceGetAttribute"):
                assert args[1] == (18 if self.backend == "cuda" else 16)
                args[0]._obj.value = int(self.integrated)
            elif name == "cuDevicePrimaryCtxRetain":
                args[0]._obj.value = 123
            elif name in ("cuMemGetInfo_v2", "hipMemGetInfo"):
                args[0]._obj.value = self.free
                args[1]._obj.value = 64 << 30
            return 0
        return Function(run)


def loader_for(**drivers):
    def load(name):
        key = "cuda" if name == "libcuda.so.1" else "hip"
        if key not in drivers:
            raise OSError("not installed")
        return drivers[key]
    return load


class RuntimeProbeTests(unittest.TestCase):
    def test_cuda_and_hip(self):
        for backend in ("cuda", "hip"):
            with self.subTest(backend=backend):
                driver = Driver(backend)
                value = probe.probe(loader_for(**{backend: driver}))
                self.assertEqual(value, dict(backend=backend, device=0,
                                            free_bytes=48 << 30, total_bytes=64 << 30))
                if backend == "cuda":
                    self.assertIn("cuDevicePrimaryCtxRelease", driver.calls)

    def test_missing_discrete_ambiguous_and_invalid(self):
        for load in (
            loader_for(),
            loader_for(cuda=Driver("cuda", integrated=False)),
            loader_for(hip=Driver("hip", integrated=False)),
            loader_for(cuda=Driver("cuda"), hip=Driver("hip")),
            loader_for(cuda=Driver("cuda", count=2)),
            loader_for(hip=Driver("hip", count=65)),
            loader_for(cuda=Driver("cuda", free=0)),
            loader_for(hip=Driver("hip", free=65 << 30)),
        ):
            with self.assertRaises(RuntimeError):
                probe.probe(load)

    def test_runtime_failure_is_not_ignored(self):
        for backend, function in (("cuda", "cuMemGetInfo_v2"), ("hip", "hipMemGetInfo")):
            driver = Driver(backend)
            driver.fail = function
            with self.assertRaises(RuntimeError):
                probe.probe(loader_for(**{backend: driver}))
            if backend == "cuda":
                self.assertEqual(driver.calls[-1], "cuDevicePrimaryCtxRelease")

    def test_ctypes_size_t_preserves_large_capacities(self):
        # ABI output uses size_t pointers, not 32-bit integer memory quantities.
        value = ctypes.c_size_t(192 << 30)
        self.assertEqual(value.value, 192 << 30)


if __name__ == "__main__":
    unittest.main()
