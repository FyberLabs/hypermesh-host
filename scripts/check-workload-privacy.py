#!/usr/bin/env python3
"""Inspect local workload privacy and optionally run a bounded Docker I/O canary.

This does not send prompts to, stop, or reconfigure an existing workload.
The canary has no GPU, model mount, network or persistent writable filesystem.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import signal
import stat
import subprocess
import time

_spec = importlib.util.spec_from_file_location("host_acceptance", Path(__file__).with_name("host-acceptance.py"))
_acceptance = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_acceptance)
publish = _acceptance.publish
DOCKER = ["/usr/bin/docker", "--host", "unix:///var/run/docker.sock"]
LIMIT = 8 << 20


class Rejected(Exception):
    pass


def command(args, data=None, timeout=20):
    """Keep diagnostics only in bounded memory; never include them in a report."""
    process = subprocess.Popen(args, stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True,
                               env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C"})
    output = bytearray()
    deadline = time.monotonic() + timeout
    try:
        if data is not None:
            process.stdin.write(data)
            process.stdin.close()
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise Rejected("command_timeout")
                for key, _ in selector.select(min(remaining, 1)):
                    chunk = os.read(key.fileobj.fileno(), 65536)
                    if not chunk:
                        selector.unregister(key.fileobj)
                    else:
                        output.extend(chunk)
                        if len(output) > LIMIT:
                            raise Rejected("command_output_limit")
        return process.wait(timeout=max(0.01, deadline-time.monotonic())), bytes(output)
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
        process.stdout.close()
        if process.stdin is not None and not process.stdin.closed:
            process.stdin.close()


def docker_json(args):
    status, output = command(DOCKER + args)
    if status:
        raise Rejected("docker_inspection_failed")
    try:
        return json.loads(output)
    except (ValueError, TypeError):
        raise Rejected("docker_inspection_invalid") from None


def privacy(config):
    if not isinstance(config, dict) or not isinstance(config.get("LogConfig"), dict):
        raise Rejected("privacy_configuration_invalid")
    limits = config.get("Ulimits")
    if not isinstance(limits, list) or any(not isinstance(item, dict) for item in limits):
        raise Rejected("privacy_configuration_invalid")
    cores = [item for item in limits if item.get("Name") == "core"]
    if (config["LogConfig"].get("Type") != "none" or len(cores) != 1 or
            type(cores[0].get("Soft")) is not int or type(cores[0].get("Hard")) is not int or
            cores[0]["Soft"] != 0 or cores[0]["Hard"] != 0):
        raise Rejected("unsafe_workload_logging_or_core_limits")
    return {"log_driver": "none", "core_soft": 0, "core_hard": 0}


def scan_files(root, needle, limit=64 << 20):
    """Search metadata/log bytes without returning content or following links."""
    total = matches = 0

    def scan_error(_error):
        raise Rejected("disk_scan_incomplete")

    for directory, _dirs, files in os.walk(root, followlinks=False, onerror=scan_error):
        for name in files:
            path = Path(directory) / name
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "rb") as stream:
                if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                    raise Rejected("disk_scan_nonregular_file")
                tail = b""
                while True:
                    chunk = stream.read(65536)
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > limit:
                        raise Rejected("disk_scan_limit")
                    matches += (tail + chunk).count(needle)
                    tail = (tail + chunk)[-(len(needle)-1):]
    return matches


def inspect(container):
    return privacy(docker_json(["inspect", "--format", "{{json .HostConfig}}", container]))


def canary(image):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image):
        raise Rejected("canary_requires_cached_image_id")
    # The payload only enters Docker's stdin. Putting it in argv, env or a
    # file would itself persist it in Docker metadata or a host file.
    needle = ("hypermesh-privacy-" + secrets.token_hex(32)).encode()
    token = secrets.token_hex(16)
    name = "hypermesh-privacy-check-" + token
    label = "hypermesh.privacy-check"
    started = int(time.time()) - 1
    result = {"kind": "synthetic_container_io", "inference_executed": False,
              "payload_sha256": hashlib.sha256(needle).hexdigest(), "cleanup": False}
    try:
        status, _ = command(DOCKER + ["create", "--name", name, "--label", label+"="+token,
            "--pull", "never", "--runtime", "runc", "--network", "none", "--read-only",
            "--tmpfs", "/tmp:rw,noexec,nosuid,size=1m", "--log-driver", "none", "--ulimit", "core=0",
            "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "32m",
            "--cpus", "0.1", "--pids-limit", "16", "-i", "--entrypoint", "/bin/sh", image,
            "-c", 'while IFS= read -r line; do printf "%s\\n" "$line"; printf "%s\\n" "$line" >&2; done'])
        if status:
            raise Rejected("canary_create_failed")
        result["effective_controls"] = inspect(name)
        status, output = command(DOCKER + ["start", "--attach", "--interactive", name], needle+b"\n")
        if status or output.count(needle) != 2:
            raise Rejected("canary_stdout_stderr_not_observed")
        result["stdout_stderr_observed"] = True
        status, output = command(DOCKER + ["logs", name])
        if needle in output or (status and b"does not support reading" not in output):
            raise Rejected("canary_logs_not_empty_or_unreadable")
        if not status and output:
            raise Rejected("canary_logs_not_empty_or_unreadable")
        result["docker_logs_empty"] = True
        root = docker_json(["info", "--format", "{{json .DockerRootDir}}"])
        if not isinstance(root, str) or not Path(root).is_absolute():
            raise Rejected("docker_root_unknown")
        containers = Path(root) / "containers"
        if not containers.is_dir():
            raise Rejected("docker_container_directory_missing")
        result["container_file_matches"] = scan_files(containers, needle)
        status, journal = command(["/usr/bin/journalctl", "--since", "@"+str(started), "--no-pager", "--output=cat"])
        if status:
            raise Rejected("journal_scan_incomplete")
        result["journal_matches"] = journal.count(needle)
        if result["container_file_matches"] or result["journal_matches"]:
            raise Rejected("canary_persisted")
    except (Rejected, OSError, subprocess.TimeoutExpired) as error:
        result["failure"] = str(error) if isinstance(error, Rejected) else "canary_observation_failed"
    finally:
        # A timed-out create can still have created the container. Remove it
        # only after its random ownership label proves it belongs to this run.
        try:
            labels = docker_json(["inspect", "--format", "{{json .Config.Labels}}", name])
            if isinstance(labels, dict) and labels.get(label) == token:
                status, _ = command(DOCKER + ["rm", "--force", name])
                result["cleanup"] = status == 0
        except (Rejected, OSError, subprocess.TimeoutExpired):
            pass
    result["passed"] = "failure" not in result and result["cleanup"]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True, help="existing workload ID or name; inspection only")
    parser.add_argument("--run-canary", action="store_true", help="create and remove a separate low-resource I/O test container")
    parser.add_argument("--canary-image", help="cached sha256 image ID containing /bin/sh; never pulled")
    parser.add_argument("--output", type=Path, required=True, help="new absolute private JSON report")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", args.container):
        parser.error("invalid container selector")
    if not args.output.is_absolute() or args.output.exists() or args.output.is_symlink():
        parser.error("output must be a new absolute path")
    if bool(args.canary_image) != args.run_canary:
        parser.error("--run-canary and --canary-image must be supplied together")
    if args.run_canary and os.geteuid() != 0:
        parser.error("root is required for complete local disk and journal scans")
    report = {"schema": 1, "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "collector_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              "existing_workload_modified": False, "passed": False}
    try:
        report["workload_controls"] = inspect(args.container)
        if args.run_canary:
            report["canary"] = canary(args.canary_image)
            report["passed"] = report["canary"]["passed"]
        else:
            report["passed"] = True
        publish(args.output, report)
    except (Rejected, OSError, subprocess.TimeoutExpired):
        parser.exit(3, "privacy acceptance could not complete; no raw workload output exported.\n")
    print(json.dumps(report))
    return 0 if report["passed"] else 3


if __name__ == "__main__":
    raise SystemExit(main())
