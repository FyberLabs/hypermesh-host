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



class AllocationDriver(Driver):
    def __init__(self, backend):
        super().__init__(backend)
        self.pointer = 1 << 34
        self.fills, self.copies, self.freed = [], [], []
        self.corrupt = False
        self.null = False

    def __getattr__(self, name):
        original = super().__getattr__(name)
        def run(*args):
            code = original(*args)
            if code:
                return code
            if name in ("cuMemAlloc_v2", "hipMalloc"):
                args[0]._obj.value = 0 if self.null else self.pointer
                self.allocated = args[1]
            elif name in ("cuMemsetD8_v2", "hipMemset"):
                self.fills.append((args[0].value, args[1], args[2]))
            elif name in ("cuMemcpyDtoH_v2", "hipMemcpyDtoH"):
                self.copies.append((args[1].value, args[2]))
                ctypes.memset(args[0], 0 if self.corrupt else 0xA5, args[2])
            elif name in ("cuMemFree_v2", "hipFree"):
                self.freed.append(args[0].value)
            return 0
        return Function(run)


def request_for(backend="cuda"):
    return dict(schema=1, backend=backend, device=0, allocation_bytes=40 << 30,
                total_bytes=64 << 30, system_bytes=64 << 30)


class AllocationTests(unittest.TestCase):
    def test_fill_entire_budget_sample_and_free_both_backends(self):
        for backend in ("cuda", "hip"):
            driver = AllocationDriver(backend)
            value = probe.allocate_memory(request_for(backend),
                loader_for(**{backend:driver}), lambda:(64 << 30, 48 << 30))
            self.assertEqual(value, dict(schema=1, backend=backend, device=0,
                allocation_bytes=40 << 30, touched_bytes=40 << 30,
                sampled_bytes=12288, cleanup_complete=True))
            self.assertEqual(driver.fills, [(driver.pointer, 0xA5, 40 << 30)])
            self.assertEqual(driver.copies, [
                (driver.pointer,4096), (driver.pointer+(20 << 30),4096),
                (driver.pointer+(40 << 30)-4096,4096)])
            self.assertEqual(driver.freed, [driver.pointer])
            if backend == "cuda":
                self.assertEqual(driver.calls[-1], "cuDevicePrimaryCtxRelease")

    def test_no_allocation_when_budget_or_device_changed(self):
        for kind in ("system", "os-pressure", "runtime-pressure", "backend", "device", "total", "ambiguous"):
            with self.subTest(kind=kind):
                driver = AllocationDriver("cuda")
                request = request_for()
                memory = lambda:(64 << 30, 48 << 30)
                if kind == "system": memory = lambda:((63 << 30)-1, 48 << 30)
                if kind == "os-pressure": memory = lambda:(64 << 30, 40 << 30)
                if kind == "runtime-pressure": driver.free = 40 << 30
                if kind == "backend": request["backend"] = "hip"
                if kind == "device": request["device"] = 1
                if kind == "total": request["total_bytes"] = (63 << 30)-1
                if kind == "ambiguous": driver.count = 2
                with self.assertRaises(RuntimeError):
                    probe.allocate_memory(request, loader_for(cuda=driver), memory)
                self.assertNotIn("cuMemAlloc_v2", driver.calls)

    def test_total_drift_keeps_live_headroom_checks(self):
        for delta in (-4096, 4096, -(1 << 30), 1 << 30):
            for backend in ("cuda", "hip"):
                driver = AllocationDriver(backend)
                request = request_for(backend)
                request["total_bytes"] += delta
                request["system_bytes"] += delta
                result = probe.allocate_memory(
                    request, loader_for(**{backend: driver}),
                    lambda: (64 << 30, 48 << 30))
                self.assertTrue(result["cleanup_complete"])
                driver = AllocationDriver(backend)
                with self.assertRaises(RuntimeError):
                    probe.allocate_memory(
                        request, loader_for(**{backend: driver}),
                        lambda: (64 << 30, 40 << 30))
                self.assertFalse(driver.fills)

    def test_operation_and_cleanup_failures_never_pass(self):
        for backend, stages in (
            ("cuda", ("cuMemAlloc_v2","cuMemsetD8_v2","cuCtxSynchronize","cuMemcpyDtoH_v2","cuMemFree_v2")),
            ("hip", ("hipMalloc","hipMemset","hipDeviceSynchronize","hipMemcpyDtoH","hipFree")),
        ):
            for stage in (*stages, "corrupt", "null"):
                with self.subTest(backend=backend, stage=stage):
                    driver = AllocationDriver(backend)
                    driver.fail = stage
                    driver.corrupt = stage == "corrupt"
                    driver.null = stage == "null"
                    with self.assertRaises(RuntimeError):
                        probe.allocate_memory(request_for(backend),
                            loader_for(**{backend:driver}), lambda:(64 << 30,48 << 30))
                    free = "cuMemFree_v2" if backend == "cuda" else "hipFree"
                    if stage not in (stages[0], "null"):
                        self.assertIn(free, driver.calls)
                    if backend == "cuda":
                        self.assertEqual(driver.calls[-1], "cuDevicePrimaryCtxRelease")

    def test_invalid_request_rejected_before_loading_driver(self):
        for key, bad in (("schema",2),("backend","metal"),("device",-1),("device",True),
                         ("allocation_bytes",11 << 30),("allocation_bytes",64 << 30),
                         ("total_bytes",1 << 63),("system_bytes",15 << 30)):
            request = request_for()
            request[key] = bad
            with self.assertRaises(RuntimeError):
                probe.allocate_memory(request, lambda _: self.fail("loaded driver"))
        request = request_for()
        request["extra"] = "ignored?"
        with self.assertRaises(RuntimeError):
            probe.allocate_memory(request, lambda _: self.fail("loaded driver"))

    def test_read_only_query_does_not_allocate(self):
        driver = AllocationDriver("cuda")
        probe.probe(loader_for(cuda=driver))
        self.assertFalse(driver.fills or driver.freed)
        self.assertNotIn("cuMemAlloc_v2",driver.calls)


if __name__ == "__main__":
    unittest.main()
