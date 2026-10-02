#!/usr/bin/env python3
"""Download a verified Linux host CLI without an existing Hypermesh CLI."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import threading
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request


# Reviewed trust anchor copied from infra/config/environments/production.yaml.
# Changes require a reviewed rotation; never learn trust from release storage.
TRUSTED_FINGERPRINT = "1540ED8CD1EC4BDF72949FC78D741FF786A369FD"
TRUSTED_KEY = b"""-----BEGIN PGP PUBLIC KEY BLOCK-----

mQINBGq8kr8BEAC0mtkwL86ZiG3SD0mhF1x1P/HAUvLGsDuVQDfqQywnXS5dwzou
ZaXLYJbcaO7UT3p/pdyWOkh0TE6e8YWZyNPELlKW/P0X17CixCVtEciJMpsBsHoN
i6aq9DPNEeFRhqB5o7B+VowHIt+3dXRTKLb2qBCcgbsSnmwPSm7Fjsh/Rq8/VqZN
e7dKh1cZZpTBBmAZQpg7Vj9PJLxCfgV2R4SVZ27uVeea2++Iz6mM6lL4DV8LKYgP
PdjxdxWCzApLslBzEfZZBKCty6lS+0b6hAlvImw7f40Es2U9acw94Cct0h0V+VH3
LQt4f0Pni1AJ4cvgw5LgOZO+kh9knxynvSwFOqdqjaR5NMAE1DVdDebca4Rlvnmz
louok8UdWgZN7EbCXuI33ETEJll+Ns03r3Qrsqs7cuwZsd2yZh+7YxsZrtNUbSnd
ezAWWSjOz4FQSO2KbORs9f2WriKKhbs7CVroSQVrGctNLwPgv551+UwLEX86ZIhZ
H3GlzKUe1mXYPnQJdJBzAzwKGEXsrTeeEmjgUJg5SfgDI+7n0JVdCA8gnKhoNM/k
5I4MR+QK2IBPRGOLfwlQxNOHNUzM1d9PKdV0QCm3rqrQ9c/dJSpTVUq/GqpppAeC
D+xLhTS4kZ+q3L1boZrrEtgUtj0iVNWfpkAYMh6bH1GSvbg1et92/egslwARAQAB
tCxIeXBlcm1lc2ggQWdlbnQgUmVsZWFzZSA8cmVsZWFzZUBoeXBlcm1lLnNoPokC
UQQTAQoAOxYhBBVA7YzR7EvfcpSfx410H/eGo2n9BQJqvJK/AhsDBQsJCAcCAiIC
BhUKCQgLAgQWAgMBAh4HAheAAAoJEI10H/eGo2n9M+sQAJUW0d+O6VBW7r29NxC+
H8plpf7CGk0+w7hQG/U6XsHwYVH7Di9e8AIlIqBQw2isO+/+sc4DXBw2rpM98re8
p7UEgWKn1nPXQHw/ghb5PK9V94SpI6Z9IuAeNhGqSGUG8sx4xo0bhtPVWE5VOz2g
ZBCKYu3VudaG2GSttOcA+9u4ALlrcAOuPO7V9ThJ49XHhFps5VQ9qcwJ/Sj7fwMk
YzBPAKFnUM3h4o2aBnyO4e5hJm0rXM6XKC2ssmpwC2tlvbkNcV8OAKkONHdXSKqa
6BKIY98d6I05QS0GrrldvsKOTrYHn4pSXg6E7+/CQ8ZW1dzAIXl8yTzretThDmCB
G1QaKUduopskRy2NAY4XhJWL6+sT+WKMq9gGXLmFHcG8v5FYTPs/MBYnbPikZCss
l+RlKffvA4oP0bxKWvfGduVIdZecGzRaiaZGI3BDMt1cARrh/Rk9340Hx6NMmu31
uFd+LWewRRcF+NntWWXwXs1wxvGI1tDhFNvhR/lbxr+VWUl/9hm0qKUmS8RcmMcg
Hf1IQvOl6IpWxcOY52ijpS5MCVkG9LV/DrqXNRxZAWZnqZINitIPwJV0XY1l5pEa
HYmpXoHk8t4pdsp+6UheLpHdNNYRXrxvwUWxafZaYOVfWagNTtfUT4sHdTYL3KDi
BTuaMpuY8awSyEzVcdD79sJY
=t2ke
-----END PGP PUBLIC KEY BLOCK-----
"""

class Rejected(Exception):
    pass


def https_url(address):
    try:
        url = urllib.parse.urlsplit(address)
        if (url.scheme != "https" or not url.hostname or url.username is not None
                or url.password is not None or url.query or url.fragment or "?" in address):
            raise ValueError()
    except ValueError:
        raise Rejected("require an HTTPS release directory without credentials or query") from None
    return address.rstrip("/") + "/"


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        parsed = urllib.parse.urlsplit(newurl)
        if parsed.scheme != "https" or parsed.username is not None or parsed.password is not None:
            raise Rejected("insecure download redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


# Unlike urllib's per-operation socket timeout, this covers DNS, redirects,
# headers and the full body, including a peer that continuously trickles bytes.
TRANSFER_DEADLINE_SECONDS = 120


@contextmanager
def transfer_deadline(seconds):
    if (threading.current_thread() is not threading.main_thread()
            or not hasattr(signal, "setitimer")):
        raise Rejected("bootstrap transfers require a Unix main thread for deadline enforcement")
    if signal.getitimer(signal.ITIMER_REAL)[0]:
        raise Rejected("cannot enforce transfer deadline while another alarm is active")
    previous = signal.getsignal(signal.SIGALRM)
    def expired(_signum, _frame):
        raise Rejected("artifact transfer exceeded its end-to-end deadline")
    signal.signal(signal.SIGALRM, expired)
    try:
        signal.setitimer(signal.ITIMER_REAL, seconds)
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)


def fetch(address, limit):
    opener = urllib.request.build_opener(HTTPSRedirect())
    with transfer_deadline(TRANSFER_DEADLINE_SECONDS):
        try:
            with opener.open(address, timeout=30) as response:
                if response.status != 200:
                    raise Rejected("download returned non-success status")
                data = response.read(limit + 1)
        except (OSError, urllib.error.URLError, ValueError):
            raise Rejected("HTTPS download failed; check connectivity and release directory") from None
    if not data or len(data) > limit:
        raise Rejected("download is empty or exceeds size limit")
    return data


def verify_signature(checksums, signature, key, fingerprint):
    fingerprint = fingerprint.upper()
    if not re.fullmatch(r"(?:[0-9A-F]{40}|[0-9A-F]{64})", fingerprint):
        raise Rejected("require a full trusted signing fingerprint")
    with tempfile.TemporaryDirectory(prefix="hypermesh-bootstrap-") as tmp:
        home = Path(tmp)
        os.chmod(home, 0o700)
        for name, data in (("key.asc", key), ("checksums.txt", checksums), ("signature", signature)):
            (home / name).write_bytes(data)
        def gpg(*args):
            try:
                return subprocess.run(
                    ["gpg", "--no-options", "--batch", "--homedir", tmp, *args],
                    capture_output=True, timeout=30)
            except (OSError, subprocess.TimeoutExpired):
                raise Rejected("GPG unavailable or timed out") from None
        if gpg("--import", str(home / "key.asc")).returncode:
            raise Rejected("trusted public key could not be imported")
        result = gpg("--status-fd", "1", "--verify", str(home / "signature"), str(home / "checksums.txt"))
        good = valid = False
        for line in result.stdout.decode("utf-8", errors="replace").splitlines():
            fields = line.split()
            if len(fields) < 2 or fields[0] != "[GNUPG:]":
                continue
            if fields[1] in {"BADSIG", "ERRSIG", "EXPSIG", "EXPKEYSIG", "REVKEYSIG", "KEYEXPIRED", "SIGEXPIRED"}:
                raise Rejected("signature or signing key is invalid, expired or revoked")
            if fields[1] == "GOODSIG":
                good = True
            if fields[1] == "VALIDSIG":
                if len(fields) < 3 or fingerprint not in (fields[2], fields[-1]):
                    raise Rejected("signature does not belong to the trusted fingerprint")
                valid = True
        if result.returncode or not good or not valid:
            raise Rejected("release checksum signature is not trusted")


def check_hash(checksums, name, payload):
    matches = []
    try:
        for line in checksums.decode("ascii").splitlines():
            fields = line.split()
            if len(fields) == 2 and fields[1] == name:
                matches.append(fields[0])
    except UnicodeDecodeError:
        raise Rejected("invalid checksum manifest") from None
    if len(matches) != 1 or not re.fullmatch(r"[0-9a-fA-F]{64}", matches[0]):
        raise Rejected("missing, duplicate or invalid artifact checksum")
    if hashlib.sha256(payload).hexdigest() != matches[0].lower():
        raise Rejected("artifact checksum mismatch")


def bootstrap(release_url, channel, minimum, arch, key, fingerprint, output, fetcher=fetch):
    base = https_url(release_url)
    if channel not in ("test", "prod") or arch not in ("arm64", "amd64") or minimum < 0:
        raise Rejected("invalid channel, architecture or minimum build")
    sums = fetcher(base + "checksums.txt", 1 << 20)
    signature = fetcher(base + "checksums.txt.sig", 1 << 20)
    verify_signature(sums, signature, key, fingerprint)
    raw = fetcher(base + "release.json", 64 << 10)
    check_hash(sums, "release.json", raw)
    try:
        meta = json.loads(raw)
        build, version, commit = meta["build"], meta["version"], meta["commit"]
        if (type(build) is not int or build <= 0 or build < minimum or meta["channel"] != channel
                or not isinstance(version, str) or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version)
                or not isinstance(commit, str) or not re.fullmatch(r"[0-9a-f]{40}", commit)):
            raise ValueError()
        release_id = version if channel == "prod" else f"{build}-{commit[:12]}"
        if (meta["release_id"] != release_id or
                urllib.parse.urlsplit(base).path.rstrip("/").split("/")[-2:] != [channel, release_id]):
            raise ValueError()
    except (ValueError, KeyError, TypeError):
        raise Rejected("signed metadata does not meet release constraints") from None
    name = f"hypermesh-deploy_{version}_linux_{arch}"
    payload = fetcher(base + name, 128 << 20)
    check_hash(sums, name, payload)
    output = Path(output)
    # Stage privately and publish without overwriting files or symlinks.
    with tempfile.NamedTemporaryFile(prefix=".hypermesh-bootstrap-", dir=output.parent) as stage:
        stage.write(payload)
        stage.flush()
        os.fchmod(stage.fileno(), 0o700)
        os.fsync(stage.fileno())
        os.link(stage.name, output)
    return meta


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-url", required=True)
    parser.add_argument("--channel", required=True, choices=["test", "prod"])
    parser.add_argument("--min-build", required=True, type=int)
    parser.add_argument("--arch", required=True, choices=["arm64", "amd64"])
    parser.add_argument("--trusted-key", type=Path, help="explicit trust override; default is the bundled reviewed key")
    parser.add_argument("--fingerprint", default=TRUSTED_FINGERPRINT)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--key-output", type=Path, help="save bundled/selected public trust to a NEW file for subsequent plans")
    args = parser.parse_args()
    if args.key_output is None:
        args.key_output = Path(str(args.output) + ".pub")
    try:
        key = TRUSTED_KEY
        if args.trusted_key:
            with args.trusted_key.open("rb") as stream:
                key = stream.read((1 << 20) + 1)
        if not key or len(key) > 1 << 20:
            raise Rejected("trusted key is empty or too large")
        meta = bootstrap(args.release_url, args.channel, args.min_build, args.arch,
                         key, args.fingerprint, args.output)
        if args.key_output:
            with tempfile.NamedTemporaryFile(prefix=".hypermesh-trust-", dir=args.key_output.parent) as stage:
                stage.write(key)
                stage.flush()
                os.fchmod(stage.fileno(), 0o644)
                os.fsync(stage.fileno())
                os.link(stage.name, args.key_output)
    except Rejected as error:
        parser.exit(1, f"bootstrap: {error}\n")
    except OSError:
        parser.exit(1, "bootstrap: cannot read trusted key or create output; existing files are never overwritten\n")
    receipt = Path(str(args.output) + ".install.json")
    key_path = args.key_output or args.trusted_key
    if key_path is None:
        parser.exit(1, "Verifier: use --key-output ./hypermesh-release.pub so guided setup can retain its trusted key.\n")
    try:
        with receipt.open("x", encoding="utf-8") as stream:
            os.chmod(receipt, 0o600)
            json.dump({"schema": 1, "release_url": args.release_url,
                       "channel": meta["channel"], "build": meta["build"],
                       "commit": meta["commit"], "trusted_key": str(key_path.resolve()),
                       "fingerprint": args.fingerprint}, stream)
            stream.flush()
            os.fsync(stream.fileno())
    except OSError:
        parser.exit(1, "Verifier: cannot save the installation receipt; use a new download directory.\n")
    print(f"Verified Linux/{args.arch} CLI: channel={meta['channel']} build={meta['build']}")
    print("Verification complete. The installer has not run yet.")
    print("Keep the installer, public key and installation receipt together.")
    print("Next: start guided setup (or resume it after an interruption):")
    print("  sudo " + __import__("shlex").quote(str(args.output.resolve())))


if __name__ == "__main__":
    main()
