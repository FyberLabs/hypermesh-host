"""UMA capacity query and explicitly requested allocation check; isolated child."""
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



def system_memory():
    """Resample OS memory immediately before allocating; never trust CLI free RAM."""
    values = {}
    with open("/proc/meminfo", encoding="ascii") as source:
        for line in source:
            fields = line.split()
            if fields and fields[0] in ("MemTotal:", "MemAvailable:"):
                if len(fields) != 3 or fields[2] != "kB" or fields[0] in values:
                    raise RuntimeError("invalid system memory")
                values[fields[0]] = int(fields[1]) * 1024
    total, available = values["MemTotal:"], values["MemAvailable:"]
    if not 0 < available <= total <= (1 << 63) - 1:
        raise RuntimeError("invalid system memory")
    return total, available


def allocation_request(value):
    fields = {"schema", "backend", "device", "allocation_bytes", "total_bytes", "system_bytes"}
    if not isinstance(value, dict) or set(value) != fields:
        raise RuntimeError("invalid allocation request")
    if any(type(value[k]) is not int for k in fields - {"backend"}):
        raise RuntimeError("invalid allocation request")
    if (value["schema"] != 1 or value["backend"] not in ("cuda", "hip")
            or not 0 <= value["device"] < 64
            or not 12 << 30 <= value["allocation_bytes"] < value["total_bytes"] <= (1 << 63) - 1
            or not 16 << 30 <= value["system_bytes"] <= (1 << 63) - 1):
        raise RuntimeError("invalid allocation request")
    return value


def allocate_memory(value, loader=c.CDLL, memory=system_memory):
    """Explicit opt-in: a single UMA allocation, full fill and sampled readback.

    This proves memory operations, not model/context fit or compute performance.
    No success is returned until allocation and retained context are released.
    """
    value = allocation_request(value)
    found = probe(loader)
    if (any(found[k] != value[k] for k in ("backend", "device"))
            or abs(found["total_bytes"] - value["total_bytes"]) > (1 << 30)):
        raise RuntimeError("runtime device changed")
    cuda_mode = value["backend"] == "cuda"
    lib = loader("libcuda.so.1" if cuda_mode else "libamdhip64.so")
    device = c.c_int()
    context = c.c_void_p()
    retained = False
    pointer = c.c_uint64() if cuda_mode else c.c_void_p()
    size = value["allocation_bytes"]
    try:
        if cuda_mode:
            call(lib, "cuDeviceGet", [c.POINTER(c.c_int), c.c_int],
                 c.byref(device), value["device"])
            call(lib, "cuDevicePrimaryCtxRetain", [c.POINTER(c.c_void_p), c.c_int],
                 c.byref(context), device)
            retained = True
            call(lib, "cuCtxSetCurrent", [c.c_void_p], context)
        else:
            call(lib, "hipSetDevice", [c.c_int], value["device"])
        free, total = c.c_size_t(), c.c_size_t()
        call(lib, "cuMemGetInfo_v2" if cuda_mode else "hipMemGetInfo",
             [c.POINTER(c.c_size_t), c.POINTER(c.c_size_t)], c.byref(free), c.byref(total))
        system, available = memory()
        reserve = max(2 << 30, (system + 9) // 10)
        if (abs(system - value["system_bytes"]) > (1 << 30) or not 0 < available <= system
                or abs(total.value - value["total_bytes"]) > (1 << 30) or free.value > total.value
                or size > min(available - reserve, free.value - (1 << 30))):
            raise RuntimeError("insufficient current memory headroom")
        if cuda_mode:
            call(lib, "cuMemAlloc_v2", [c.POINTER(c.c_uint64), c.c_size_t],
                 c.byref(pointer), size)
        else:
            call(lib, "hipMalloc", [c.POINTER(c.c_void_p), c.c_size_t],
                 c.byref(pointer), size)
        if not pointer.value:
            raise RuntimeError("runtime returned a null allocation")
        try:
            if cuda_mode:
                call(lib, "cuMemsetD8_v2", [c.c_uint64, c.c_ubyte, c.c_size_t],
                     pointer, 0xA5, size)
            else:
                call(lib, "hipMemset", [c.c_void_p, c.c_int, c.c_size_t],
                     pointer, 0xA5, size)
            call(lib, "cuCtxSynchronize" if cuda_mode else "hipDeviceSynchronize", [])
            # The entire allocation is written; sample both ends and its middle.
            for offset in (0, size // 2, size - 4096):
                sample = (c.c_ubyte * 4096)()
                source = (c.c_uint64 if cuda_mode else c.c_void_p)(pointer.value + offset)
                call(lib, "cuMemcpyDtoH_v2" if cuda_mode else "hipMemcpyDtoH",
                     [c.c_void_p, c.c_uint64 if cuda_mode else c.c_void_p, c.c_size_t],
                     sample, source, len(sample))
                if bytes(sample) != bytes([0xA5]) * len(sample):
                    raise RuntimeError("runtime readback mismatch")
        finally:
            call(lib, "cuMemFree_v2" if cuda_mode else "hipFree",
                 [c.c_uint64 if cuda_mode else c.c_void_p], pointer)
    finally:
        if retained:
            call(lib, "cuDevicePrimaryCtxRelease", [c.c_int], device)
    return dict(schema=1, backend=value["backend"], device=value["device"],
                allocation_bytes=size, touched_bytes=size, sampled_bytes=3 * 4096,
                cleanup_complete=True)


def main():
    if sys.argv[1:] == []:
        return probe()
    if sys.argv[1:] != ["--allocate"]:
        raise RuntimeError("unsupported mode")
    raw = sys.stdin.read(4097)
    if len(raw) > 4096:
        raise RuntimeError("allocation request too large")
    return allocate_memory(json.loads(raw))


if __name__ == "__main__":
    try:
        print(json.dumps(main()))
    except (OSError, AttributeError, RuntimeError, ValueError, KeyError, TypeError):
        # Driver diagnostics may include local details. Parent emits remediation.
        sys.exit(1)
