"""Read-only UMA runtime capacity probe; executed as an isolated bounded child."""
import ctypes as c
import json
import sys


def call(lib, name, types, *args):
    fn = getattr(lib, name)
    fn.argtypes = types
    fn.restype = c.c_int
    if fn(*args) != 0:
        raise RuntimeError("runtime query failed")


def cuda(lib):
    call(lib, "cuInit", [c.c_uint], 0)
    count = c.c_int()
    call(lib, "cuDeviceGetCount", [c.POINTER(c.c_int)], c.byref(count))
    result = []
    if not 0 <= count.value <= 64:
        raise RuntimeError("invalid device count")
    for ordinal in range(count.value):
        device = c.c_int()
        call(lib, "cuDeviceGet", [c.POINTER(c.c_int), c.c_int], c.byref(device), ordinal)
        integrated = c.c_int()
        # CU_DEVICE_ATTRIBUTE_INTEGRATED, not unified virtual addressing.
        call(lib, "cuDeviceGetAttribute", [c.POINTER(c.c_int), c.c_int, c.c_int],
             c.byref(integrated), 18, device)
        if integrated.value != 1:
            continue
        ctx = c.c_void_p()
        call(lib, "cuDevicePrimaryCtxRetain", [c.POINTER(c.c_void_p), c.c_int],
             c.byref(ctx), device)
        try:
            call(lib, "cuCtxSetCurrent", [c.c_void_p], ctx)
            free, total = c.c_size_t(), c.c_size_t()
            call(lib, "cuMemGetInfo_v2", [c.POINTER(c.c_size_t), c.POINTER(c.c_size_t)],
                 c.byref(free), c.byref(total))
            result.append(dict(backend="cuda", device=ordinal, free_bytes=free.value,
                               total_bytes=total.value))
        finally:
            call(lib, "cuDevicePrimaryCtxRelease", [c.c_int], device)
    return result


def hip(lib):
    call(lib, "hipInit", [c.c_uint], 0)
    count = c.c_int()
    call(lib, "hipGetDeviceCount", [c.POINTER(c.c_int)], c.byref(count))
    result = []
    if not 0 <= count.value <= 64:
        raise RuntimeError("invalid device count")
    for device in range(count.value):
        integrated = c.c_int()
        # hipDeviceAttributeIntegrated = 16 (HIP enum differs from CUDA).
        call(lib, "hipDeviceGetAttribute", [c.POINTER(c.c_int), c.c_int, c.c_int],
             c.byref(integrated), 16, device)
        if integrated.value != 1:
            continue
        call(lib, "hipSetDevice", [c.c_int], device)
        free, total = c.c_size_t(), c.c_size_t()
        call(lib, "hipMemGetInfo", [c.POINTER(c.c_size_t), c.POINTER(c.c_size_t)],
             c.byref(free), c.byref(total))
        result.append(dict(backend="hip", device=device, free_bytes=free.value,
                           total_bytes=total.value))
    return result


def probe(loader=c.CDLL):
    found = []
    for name, query in (("libcuda.so.1", cuda), ("libamdhip64.so", hip)):
        try:
            lib = loader(name)
        except OSError:
            continue
        found.extend(query(lib))
    if len(found) != 1:
        raise RuntimeError("require exactly one supported integrated accelerator")
    value = found[0]
    if not 0 < value["free_bytes"] <= value["total_bytes"] <= (1 << 63) - 1:
        raise RuntimeError("invalid runtime memory measurement")
    return value


if __name__ == "__main__":
    try:
        print(json.dumps(probe()))
    except (OSError, AttributeError, RuntimeError):
        # Driver diagnostics may include local details. Parent emits remediation.
        sys.exit(1)
