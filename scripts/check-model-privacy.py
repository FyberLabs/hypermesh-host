#!/usr/bin/env python3
"""Send one synthetic request to an explicitly approved disposable lab workload.

The endpoint is derived from the selected local Docker container, never an
operator-supplied URL. Prompt and output remain in bounded memory; only hashes,
usage and persistence counts are exported. No lease/configuration is changed.
"""
import argparse
import datetime
import hashlib
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import time
import urllib.request

_spec = importlib.util.spec_from_file_location("workload_privacy", Path(__file__).with_name("check-workload-privacy.py"))
privacy = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(privacy)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise privacy.Rejected("model_endpoint_redirected")


def request_json(url, payload=None):
    # Never send the synthetic prompt through an environment-configured proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    data = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with opener.open(request, timeout=120) as response:
        raw = response.read((1 << 20) + 1)
        if response.status != 200 or len(raw) > 1 << 20:
            raise privacy.Rejected("model_response_invalid_or_too_large")
        return json.loads(raw)


def endpoint(networks):
    if not isinstance(networks, dict) or set(networks) != {"hypermesh-workload"}:
        raise privacy.Rejected("model_probe_requires_workload_network_only")
    network = networks["hypermesh-workload"]
    if not isinstance(network, dict):
        raise privacy.Rejected("model_network_invalid")
    try:
        address = ipaddress.ip_address(network.get("IPAddress", ""))
    except ValueError:
        raise privacy.Rejected("model_address_invalid") from None
    if address.version != 4 or not address.is_private or address.is_loopback or address.is_unspecified:
        raise privacy.Rejected("model_address_invalid")
    return "http://" + str(address) + ":8080"


def collect(container, request=request_json):
    # Resolve to an immutable container ID before any subsequent inspection.
    cid = privacy.docker_json(["inspect", "--format", "{{json .Id}}", container])
    if not isinstance(cid, str) or not re.fullmatch(r"[0-9a-f]{64}", cid):
        raise privacy.Rejected("model_container_id_invalid")
    controls = privacy.inspect(cid)
    before = privacy.docker_json(["inspect", "--format", "{{json .State}}", cid])
    if not isinstance(before, dict) or before.get("Running") is not True or before.get("Restarting") is not False:
        raise privacy.Rejected("model_container_not_ready")
    networks = privacy.docker_json(["inspect", "--format", "{{json .NetworkSettings.Networks}}", cid])
    base = endpoint(networks)
    image = privacy.docker_json(["inspect", "--format", "{{json .Image}}", cid])
    if not isinstance(image, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", image):
        raise privacy.Rejected("model_image_id_invalid")
    models = request(base + "/v1/models")
    rows = models.get("data") if isinstance(models, dict) else None
    if not isinstance(rows, list) or len(rows) != 1 or not isinstance(rows[0], dict):
        raise privacy.Rejected("model_identity_ambiguous")
    model = rows[0].get("id")
    if not isinstance(model, str) or not model or len(model) > 1024:
        raise privacy.Rejected("model_identity_invalid")
    input_marker = "hypermesh_privacy_input_" + secrets.token_hex(16)
    output_marker = "hypermesh_privacy_output_" + secrets.token_hex(16)
    prompt = "Synthetic privacy acceptance. Input marker: " + input_marker + ". Reply with exactly " + output_marker + " and nothing else."
    payload = {"model": model, "messages": [{"role": "user", "content": prompt}],
               "max_tokens": 96, "temperature": 0, "stream": False}
    started = int(time.time()) - 1
    clock = time.monotonic()
    response = request(base + "/v1/chat/completions", payload)
    elapsed = round((time.monotonic() - clock) * 1000)
    try:
        completion = response["choices"][0]["message"]["content"]
        usage = response["usage"]
        completion_tokens, prompt_tokens = usage["completion_tokens"], usage["prompt_tokens"]
    except (KeyError, IndexError, TypeError):
        raise privacy.Rejected("model_completion_invalid") from None
    if (not isinstance(completion, str) or output_marker not in completion or
            type(completion_tokens) is not int or completion_tokens <= 0 or
            type(prompt_tokens) is not int or prompt_tokens <= 0):
        raise privacy.Rejected("model_did_not_return_measured_marker")
    # Allow asynchronous log delivery before the bounded persistence checks.
    time.sleep(2)
    needles = [input_marker.encode(), output_marker.encode()]
    status, logs = privacy.command(privacy.DOCKER + ["logs", cid])
    if any(needle in logs for needle in needles) or (status and b"does not support reading" not in logs) or (not status and logs):
        raise privacy.Rejected("model_docker_logs_not_empty")
    root = privacy.docker_json(["info", "--format", "{{json .DockerRootDir}}"])
    if not isinstance(root, str) or not Path(root).is_absolute() or not (Path(root) / "containers").is_dir():
        raise privacy.Rejected("model_docker_directory_unknown")
    disk_counts = [privacy.scan_files(Path(root) / "containers", needle) for needle in needles]
    status, journal = privacy.command(["/usr/bin/journalctl", "--since", "@"+str(started), "--no-pager", "--output=cat"])
    if status:
        raise privacy.Rejected("model_journal_scan_incomplete")
    journal_counts = [journal.count(needle) for needle in needles]
    if any(disk_counts) or any(journal_counts):
        raise privacy.Rejected("model_prompt_or_output_persisted")
    after = privacy.docker_json(["inspect", "--format", "{{json .State}}", cid])
    stable = (isinstance(after, dict) and after.get("Running") is True and
              before.get("StartedAt") == after.get("StartedAt") and
              before.get("Pid") == after.get("Pid"))
    if not stable or privacy.inspect(cid) != controls:
        raise privacy.Rejected("model_workload_changed_during_probe")
    return {"schema": 1, "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "collector_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "privacy_collector_sha256": hashlib.sha256(Path(__file__).with_name("check-workload-privacy.py").read_bytes()).hexdigest(),
            "scope": "approved disposable lab lease, direct local container inference",
            "inference_executed": True, "public_renter_or_payment_journey_executed": False,
            "image_id": image, "model_alias_sha256": hashlib.sha256(model.encode()).hexdigest(),
            "prompt_sha256": hashlib.sha256(prompt.encode()).hexdigest(),
            "completion_sha256": hashlib.sha256(completion.encode()).hexdigest(),
            "completion_tokens": completion_tokens, "prompt_tokens": prompt_tokens,
            "elapsed_ms": elapsed, "output_marker_returned": True, "workload_controls": controls,
            "docker_logs_empty": True, "container_file_matches": {"input": disk_counts[0], "output": disk_counts[1]},
            "journal_matches": {"input": journal_counts[0], "output": journal_counts[1]},
            "workload_running_unchanged": True, "passed": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True)
    parser.add_argument("--run", action="store_true", help="explicitly approve one synthetic request to this disposable lab workload")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not args.run or os.geteuid() != 0:
        parser.error("root and --run are required; use only an explicitly approved disposable lab lease")
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", args.container):
        parser.error("invalid container selector")
    if not args.output.is_absolute() or args.output.exists() or args.output.is_symlink():
        parser.error("output must be a new absolute path")
    try:
        report = collect(args.container)
        privacy.publish(args.output, report)
    except (privacy.Rejected, OSError, ValueError, TypeError) as error:
        code = str(error) if isinstance(error, privacy.Rejected) else "model_observation_failed"
        parser.exit(3, "model privacy acceptance incomplete: " + code + "; no prompt or response exported.\n")
    print(json.dumps(report))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
