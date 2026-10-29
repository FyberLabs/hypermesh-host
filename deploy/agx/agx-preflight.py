#!/usr/bin/env python3
"""Fail closed unless this machine matches the AGX soak image.

Pins already written down for AGX64-1. This does not invent a JetPack,
CUDA, or kernel version, and it does not claim a check ran on the box
unless this process is that box.

Sources:
  hypermesh-host deploy/agx/first-boot.md
  hypermesh-host deploy/agx/workload/README.md
  hypermesh-docs host-enroll.md
  hypermesh-bench packs/thin-v1/pack.yaml
  internal/deploy/preflight.go (CLI pins; checked for drift in CI)

TensorRT is not required. llama.cpp CUDA decode does not need it.
Kernel WireGuard is not required (the cottage host WireGuard join uses wireguard-go;
CONFIG_WIREGUARD stays unset). Clock state comes from online CPU/GPU scaling limits and EMC lock controls.
Missing or unreadable settings fail closed; current frequency is not evidence
that clock locking is disabled.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable

# JetPack 7.2.1 is the image name. The on-box confirm the docs give is
# /etc/nv_tegra_release: R39 / REVISION 2.1.
L4T = "R39.2.1"
CUDA = "13.2.1"
# Documented on-box rescue, not the soak default.
CUDA_RESCUE = "13.0.0"
KERNEL_PREFIX = "6.8"
UBUNTU = "24.04.5"
ARCH = "aarch64"
MEMORY_MIN_BYTES = 56 * (1 << 30)
MEMORY_MAX_BYTES = 66 * (1 << 30)
NVPMODEL_ID = 2
POWER_MODE = "30W"
DEFAULT_TARGET = "multi-user.target"
INSTALLED_PACKAGES = ("gdm3", "nvidia-l4t-3d-core")
BOOT_CONTROL = "/etc/nv_boot_control.conf"
NV_TEGRA_RELEASE = "/etc/nv_tegra_release"
OS_RELEASE = "/etc/os-release"
DOCKER_DAEMON = "/etc/docker/daemon.json"

EXIT_READY = 0
EXIT_NOT_READY = 3

Run = Callable[[list[str]], tuple[int, str, str]]
ReadText = Callable[[str], str | None]
Which = Callable[[str], str | None]
Uname = Callable[[], tuple[str, str]]


def _run(cmd: list[str]) -> tuple[int, str, str]:
    try:
        proc = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=8,
            check=False,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired, OSError):
        return 127, "", ""
    return proc.returncode, proc.stdout or "", proc.stderr or ""


def _read_text(path: str) -> str | None:
    try:
        text = Path(path).read_text(encoding="utf-8", errors="replace").strip()
    except OSError:
        return None
    return text or None


def _which(name: str) -> str | None:
    return shutil.which(name)


def _uname() -> tuple[str, str]:
    info = os.uname()
    return info.machine, info.release


def parse_l4t(text: str | None) -> str | None:
    if not text:
        return None
    first = text.splitlines()[0].strip()
    release = re.search(r"R(\d+)", first)
    revision = re.search(r"REVISION:\s*([0-9.]+)", first)
    if release and revision:
        return f"R{release.group(1)}.{revision.group(1)}"
    return None


def parse_cuda_text(nvcc_out: str, version_json: str | None, version_txt: str | None) -> str | None:
    if version_json:
        try:
            data = json.loads(version_json)
        except json.JSONDecodeError:
            data = None
        if isinstance(data, dict):
            cuda = data.get("cuda")
            if isinstance(cuda, dict) and cuda.get("version"):
                return str(cuda["version"])
            if data.get("version"):
                return str(data["version"])
    if version_txt:
        match = re.search(r"CUDA Version\s+([0-9.]+)", version_txt, re.I)
        if match:
            return match.group(1)
        return version_txt.splitlines()[0].strip() or None
    return None


def parse_nvpmodel(raw: str | None) -> tuple[int | None, str | None]:
    if not raw:
        return None, None
    nvp_id: int | None = None
    label: str | None = None
    for line in raw.splitlines():
        line = line.strip()
        mode = re.search(r"NV Power Mode:\s*(\S+)", line, re.I)
        if mode:
            token = mode.group(1)
            label = token
            watts = re.search(r"(\d+)\s*W", token, re.I)
            if watts:
                label = f"{watts.group(1)}W"
            continue
        if re.fullmatch(r"-?\d+", line):
            nvp_id = int(line)
    if label and label.upper().startswith("MODE_"):
        rest = label.split("_", 1)[1]
        watts = re.search(r"(\d+)", rest)
        if watts:
            label = f"{watts.group(1)}W"
    return nvp_id, label


def clock_lock_state(read_text: ReadText) -> bool | None:
    """True locked, False dynamic, None unproven; never infer from current load."""
    online = read_text("/sys/devices/system/cpu/online")
    if not online:
        return None
    try:
        cpus = set()
        for part in online.split(","):
            ends = part.strip().split("-")
            if len(ends) > 2:
                return None
            first, last = int(ends[0]), int(ends[-1])
            if not 0 <= first <= last <= 255:
                return None
            cpus.update(range(first, last + 1))
        bases = [f"/sys/devices/system/cpu/cpu{cpu}/cpufreq/scaling_" for cpu in sorted(cpus)]
        limits = [(int(read_text(base + "min_freq")), int(read_text(base + "max_freq"))) for base in bases]
        gpu = "/sys/devices/platform/17000000.gpu/devfreq/17000000.gpu/"
        limits.append((int(read_text(gpu + "min_freq")), int(read_text(gpu + "max_freq"))))
        emc = int(read_text("/sys/kernel/debug/bpmp/debug/clk/emc/mrq_rate_locked"))
        halt = int(read_text("/sys/kernel/debug/bpmp/debug/bwmgr/bwmgr_halt"))
    except (TypeError, ValueError):
        return None
    if emc not in (0, 1) or halt not in (0, 1) or any(lo < 0 or hi <= 0 or lo > hi for lo, hi in limits):
        return None
    return emc == 1 or halt == 1 or any(lo == hi for lo, hi in limits)


def parse_os_release(text: str | None) -> str | None:
    if not text:
        return None
    values: dict[str, str] = {}
    for line in text.splitlines():
        if "=" not in line or line.startswith("#"):
            continue
        key, _, raw = line.partition("=")
        values[key.strip()] = raw.strip().strip('"').strip("'")
    version = values.get("VERSION") or ""
    version_id = values.get("VERSION_ID") or ""
    for candidate in (version, version_id):
        match = re.search(r"\d+\.\d+(?:\.\d+)?", candidate)
        if match:
            return match.group(0)
    return None


def nvidia_runtime_configured(daemon_json: str | None, runtime_bin: str | None, which: Which = _which) -> bool:
    if not runtime_bin or not daemon_json:
        return False
    try:
        data = json.loads(daemon_json)
    except json.JSONDecodeError:
        return False
    runtimes = data.get("runtimes") if isinstance(data, dict) else None
    entry = runtimes.get("nvidia") if isinstance(runtimes, dict) else None
    if not isinstance(entry, dict) or not isinstance(entry.get("path"), str):
        return False
    configured = which(entry["path"])
    return bool(configured) and os.path.realpath(configured) == os.path.realpath(runtime_bin)


def _check(checks: list[dict[str, Any]], name: str, ok: bool, detail: str) -> None:
    checks.append({"name": name, "ok": bool(ok), "detail": detail})


def cuda_eligible(value: Any) -> bool:
    if not isinstance(value, str) or not re.fullmatch(r"13\.2\.[0-9]{1,10}", value):
        return False
    return 1 <= int(value.split(".")[-1]) <= 4294967295


def evaluate(facts: dict[str, Any]) -> dict[str, Any]:
    """Return ``{ok, checks}``. ``ok`` is false when any check failed."""

    checks: list[dict[str, Any]] = []
    arch = facts.get("arch")
    _check(
        checks,
        "arch",
        arch == ARCH,
        f"uname -m is {arch!r}; soak host is {ARCH}",
    )
    memory = facts.get("memory_bytes")
    _check(checks, "agx_memory", type(memory) is int and MEMORY_MIN_BYTES <= memory <= MEMORY_MAX_BYTES,
           f"MemTotal is {memory!r} bytes; AGX 64 GB range is 56–66 GiB")
    l4t = facts.get("l4t")
    _check(
        checks,
        "l4t",
        l4t == L4T,
        f"/etc/nv_tegra_release is {l4t!r}; pinned {L4T} (JetPack 7.2.1)",
    )
    kernel = str(facts.get("kernel") or "")
    kernel_ok = kernel == KERNEL_PREFIX or kernel.startswith(KERNEL_PREFIX + ".") or kernel.startswith(
        KERNEL_PREFIX + "-"
    )
    _check(
        checks,
        "kernel",
        kernel_ok,
        f"uname -r is {kernel or 'missing'!r}; pin is kernel {KERNEL_PREFIX}",
    )
    ubuntu = facts.get("ubuntu")
    _check(
        checks,
        "ubuntu",
        ubuntu == UBUNTU,
        f"os-release version is {ubuntu!r}; sample rootfs pin is {UBUNTU}",
    )
    cuda = facts.get("cuda")
    if cuda == CUDA_RESCUE:
        cuda_detail = (
            f"CUDA {CUDA_RESCUE} is the documented rescue, not the soak default ({CUDA})"
        )
    else:
        cuda_detail = f"CUDA is {cuda!r}; eligible family is 13.2 patch >= 1; fresh certification required"
    _check(checks, "cuda", cuda_eligible(cuda), cuda_detail)
    target = facts.get("default_target")
    _check(
        checks,
        "headless",
        target == DEFAULT_TARGET,
        f"systemctl get-default is {target!r}; headless pin is {DEFAULT_TARGET}",
    )
    packages = facts.get("packages") if isinstance(facts.get("packages"), dict) else {}
    for name in INSTALLED_PACKAGES:
        installed = packages.get(name) is True
        _check(
            checks,
            f"package:{name}",
            installed,
            f"{name} installed"
            if installed
            else f"{name} is not installed; first-boot leaves it on the NVIDIA image",
        )
    boot = facts.get("boot_control") is True
    _check(
        checks,
        "nv_boot_control",
        boot,
        f"{BOOT_CONTROL} present" if boot else f"{BOOT_CONTROL} missing or empty",
    )
    nvp_id = facts.get("nvpmodel_id")
    power = facts.get("power_mode")
    _check(
        checks,
        "nvpmodel",
        nvp_id == NVPMODEL_ID and power == POWER_MODE,
        f"nvpmodel id {nvp_id!r} mode {power!r}; pin is {NVPMODEL_ID} / {POWER_MODE}",
    )
    clocks = facts.get("jetson_clocks")
    if facts.get("jetson_clocks_present") is not True:
        _check(checks, "jetson_clocks", False, "jetson_clocks missing")
    elif clocks is True:
        _check(
            checks,
            "jetson_clocks",
            False,
            "jetson_clocks is running; thin-v1 keeps it off",
        )
    elif clocks is False:
        _check(checks, "jetson_clocks", True, "jetson_clocks is not running")
    else:
        _check(
            checks,
            "jetson_clocks",
            False,
            "CPU/GPU scaling limits or EMC lock controls are unavailable or invalid",
        )
    _check(
        checks,
        "tegrastats",
        facts.get("tegrastats") is True,
        "tegrastats on PATH" if facts.get("tegrastats") else "tegrastats missing (JetPack tool; no nvidia-smi)",
    )
    _check(
        checks,
        "docker",
        facts.get("docker") is True,
        "local Docker daemon responded" if facts.get("docker") else "local Docker daemon query failed or returned invalid runtime information",
    )
    _check(
        checks,
        "nvidia_runtime",
        facts.get("nvidia_runtime") is True,
        "nvidia container runtime configured"
        if facts.get("nvidia_runtime")
        else "nvidia runtime missing; first-boot runs nvidia-ctk runtime configure --runtime=docker",
    )
    _check(
        checks,
        "wireguard_tools",
        facts.get("wireguard_tools") is True,
        "wg on PATH" if facts.get("wireguard_tools") else "wireguard-tools missing (first-boot install)",
    )
    bench = facts.get("llama_bench") is True
    _check(
        checks,
        "llama_bench",
        bench or facts.get("nvidia_runtime") is True,
        "llama-bench on PATH"
        if bench
        else "native llama-bench not on PATH; OCI soak still needs the nvidia runtime",
    )
    _check(
        checks,
        "tensorrt",
        True,
        "not required; llama.cpp CUDA decode does not need TensorRT",
    )
    return {"policy_version": "agx-host-eligibility/v2", "certification_required": True,
            "ok": all(item["ok"] for item in checks), "checks": checks}


def collect_facts(
    *,
    run: Run | None = None,
    read_text: ReadText | None = None,
    which: Which | None = None,
    uname: Uname | None = None,
) -> dict[str, Any]:
    run = run or _run
    read_text = read_text or _read_text
    which = which or _which
    uname = uname or _uname
    machine, release = uname()
    code, nvcc_out, _ = run(["nvcc", "--version"])
    if code != 0:
        nvcc_out = ""
    nvp_code, nvp_out, nvp_err = run(["nvpmodel", "-q"])
    nvp_raw = ((nvp_out or nvp_err).strip() or None) if nvp_code == 0 else None
    nvp_id, power = parse_nvpmodel(nvp_raw)
    clocks_bin = which("jetson_clocks")
    clocks_code, clocks_out, clocks_err = run(["jetson_clocks", "--show"]) if clocks_bin else (127, "", "")
    packages: dict[str, bool] = {}
    for name in INSTALLED_PACKAGES:
        status_code, status_out, _ = run(["dpkg-query", "-W", f"-f=${{Status}}", name])
        packages[name] = status_code == 0 and status_out.strip() == "install ok installed"
    boot = read_text(BOOT_CONTROL)
    docker_code, docker_out, _ = run(["docker", "--host=unix:///var/run/docker.sock", "info", "--format", "{{json .Runtimes}}"])
    try:
        runtimes = json.loads(docker_out) if docker_code == 0 else None
    except json.JSONDecodeError:
        runtimes = None
    docker_ready = isinstance(runtimes, dict)
    daemon = json.dumps({"runtimes": runtimes}) if docker_ready else None
    runtime_bin = which("nvidia-container-runtime")
    memory_match = re.search(r"^MemTotal:\s+([0-9]+)\s+kB\s*$", read_text("/proc/meminfo") or "", re.M)
    return {
        "memory_bytes": int(memory_match.group(1)) * 1024 if memory_match else None,
        "arch": machine,
        "kernel": release,
        "l4t": parse_l4t(read_text(NV_TEGRA_RELEASE)),
        "ubuntu": parse_os_release(read_text(OS_RELEASE)),
        "cuda": parse_cuda_text(
            nvcc_out,
            read_text("/usr/local/cuda/version.json"),
            read_text("/usr/local/cuda/version.txt"),
        ),
        "default_target": (run(["systemctl", "get-default"])[1] or "").strip() or None,
        "packages": packages,
        "boot_control": bool(boot),
        "nvpmodel_id": nvp_id,
        "power_mode": power,
        "jetson_clocks_present": clocks_bin is not None,
        "jetson_clocks": clock_lock_state(read_text) if clocks_bin and clocks_code == 0 else None,
        "tegrastats": which("tegrastats") is not None,
        "docker": docker_ready,
        "nvidia_runtime": nvidia_runtime_configured(daemon, runtime_bin, which),
        "wireguard_tools": which("wg") is not None,
        "llama_bench": which("llama-bench") is not None,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--write",
        type=Path,
        default=None,
        help="Also write the JSON report to this path",
    )
    args = parser.parse_args(argv)
    report = evaluate(collect_facts())
    text = json.dumps(report, indent=2) + "\n"
    if args.write:
        args.write.parent.mkdir(parents=True, exist_ok=True)
        args.write.write_text(text, encoding="utf-8")
    sys.stdout.write(text)
    if report["ok"]:
        return EXIT_READY
    failed = [item for item in report["checks"] if not item["ok"]]
    print("AGX preflight failed closed:", file=sys.stderr)
    for item in failed:
        print(f"  {item['name']}: {item['detail']}", file=sys.stderr)
    return EXIT_NOT_READY


if __name__ == "__main__":
    raise SystemExit(main())
