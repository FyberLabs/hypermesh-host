#!/usr/bin/env python3
"""Collect live Linux acceptance evidence; never installs, enrolls, reboots or sells."""
import argparse
import datetime
import json
import os
from pathlib import Path
import re
import selectors
import signal
import stat
import subprocess
import tempfile
import time

LIMIT = 2 << 20


def run_json(argv, timeout=120):
    """Bound both output and the whole command; never print child diagnostics."""
    process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                               start_new_session=True, env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C"})
    deadline = time.monotonic() + timeout
    result = bytearray()
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ValueError("status command timed out")
                for key, _ in selector.select(min(remaining, 1)):
                    part = os.read(key.fileobj.fileno(), 65536)
                    if not part:
                        selector.unregister(key.fileobj)
                        continue
                    result.extend(part)
                    if len(result) > LIMIT:
                        raise ValueError("status output too large")
        status = process.wait(timeout=max(0.01, deadline - time.monotonic()))
        if status:
            raise ValueError("status command failed; inspect its normal status output locally")
        return json.loads(result)
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
        process.stdout.close()


def read_private_json(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise ValueError("plan/evidence must be a private regular file owned by the caller")
        raw = stream.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise ValueError("plan/evidence too large")
    return json.loads(raw)


def assess(status, expected):
    if (not isinstance(status, dict) or status.get("schema") != 1 or
        status.get("plan_sha256") != expected or status.get("attention_required") is not False or
        status.get("activation_allowed") is not False or status.get("selling_allowed") is not False or
        status.get("plane_sell_state") != "paused"):
        raise ValueError("live readiness must pass and the host must remain paused")
    gates = status.get("gates")
    required = {"installed_files", "identity", "certification_history", "activation_marker",
                "reboot", "control_plane", "class", "intake", "certification",
                "schedule", "heartbeat", "selling_state", "agent_service", "aiuditor"}
    if not isinstance(gates, list) or not gates or any(not isinstance(g, dict) or g.get("state") != "passed" for g in gates):
        raise ValueError("live readiness has blocked or malformed gates")
    if not required.issubset({g.get("id") for g in gates}):
        raise ValueError("live readiness gates are incomplete")
    age = status.get("heartbeat_age_seconds")
    if type(age) is not int or not 0 <= age < 120:
        raise ValueError("a fresh heartbeat is required")
    history = status.get("certification_history", {})
    if history.get("collection") != "collected" or history.get("state") != "active":
        raise ValueError("active certification evidence is required")
    return {"gates": sorted(g["id"] for g in gates), "heartbeat_age_seconds": age,
            "plane_sell_state": "paused", "selling_enabled_by_acceptance": False}


def publish(path, report):
    path = Path(path)
    with tempfile.NamedTemporaryFile(prefix=".host-acceptance-", dir=path.parent) as stage:
        os.fchmod(stage.fileno(), 0o600)
        stage.write((json.dumps(report, indent=2) + "\n").encode())
        stage.flush()
        os.fsync(stage.fileno())
        os.link(stage.name, path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--installer", type=Path, required=True)
    parser.add_argument("--certification-plan", type=Path, required=True)
    parser.add_argument("--phase", choices=("initial-ready", "after-maintenance", "after-repair"), required=True)
    parser.add_argument("--maintenance-plan", type=Path)
    parser.add_argument("--recovery-plan", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if os.geteuid() != 0:
            raise ValueError("run as root to read existing private host state")
        plan = read_private_json(args.certification_plan)
        digest = plan.get("plan_sha256", "")
        if not isinstance(digest, str) or re.fullmatch("[0-9a-f]{64}", digest) is None:
            raise ValueError("invalid certification plan digest")
        info = args.installer.lstat()
        if not args.installer.is_absolute() or not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("use the absolute root-owned verified installer without writable permissions")
        if args.phase != "initial-ready":
            if args.maintenance_plan is None:
                raise ValueError("maintenance plan required after maintenance/repair")
            maintenance = read_private_json(args.maintenance_plan)
            sha = maintenance.get("plan_sha256", "")
            if not isinstance(sha, str) or re.fullmatch("[0-9a-f]{64}", sha) is None:
                raise ValueError("invalid maintenance digest")
            if maintenance.get("certification", {}).get("plan_sha256") != digest:
                raise ValueError("maintenance belongs to another certification")
            journal = read_private_json(Path("/var/lib/hypermesh/installer") / ("maintenance-" + sha + ".json"))
            if journal.get("plan_sha256") != sha or journal.get("state") != "complete":
                raise ValueError("maintenance resume has not completed")
        if args.phase == "after-repair":
            if args.recovery_plan is None:
                raise ValueError("repair plan required")
            recovery = read_private_json(args.recovery_plan)
            sha = recovery.get("plan_sha256", "")
            if not isinstance(sha, str) or re.fullmatch("[0-9a-f]{64}", sha) is None or recovery.get("operation") != "repair":
                raise ValueError("invalid repair plan")
            if recovery.get("agent", {}).get("plan_sha256") != plan.get("agent", {}).get("plan_sha256"):
                raise ValueError("repair belongs to another installation")
            journal = read_private_json(Path("/var/lib/hypermesh/installer") / ("recovery-" + sha) / "journal.json")
            if journal.get("plan_sha256") != sha or journal.get("state") != "complete":
                raise ValueError("repair has not completed")
        status = run_json([str(args.installer), "host-status", "--plan", str(args.certification_plan)])
        summary = assess(status, digest)
        report = {"schema": 1, "phase": args.phase, "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                  "readiness_passed": True, "physical_soak_completed": False, **summary}
        publish(args.output, report)
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
        parser.exit(1, "acceptance: evidence incomplete or unsafe; inspect host-status and the selected private plans locally. No host changes made.\n")
    print("Readiness evidence saved. Host remains paused. Physical soak and operator observations are separate.")


if __name__ == "__main__":
    main()
