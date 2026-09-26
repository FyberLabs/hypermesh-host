#!/usr/bin/env python3
"""Check every az flag used by the release publisher against azure-cli --help.

The publish tests stub az, so a flag the real CLI rejects (keyvault
secret show --auth-mode) stays green until this script runs. It reads
the release workflow and the publish scripts, installs nothing, and
requires az on PATH.
"""

import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FILES = [
    ROOT / "packaging/release/publish-agent.sh",
    ROOT / "packaging/release/layout.sh",
    ROOT / ".github/workflows/release.yml",
    ROOT / ".github/workflows/ci.yml",
]

# Function name -> (az command words before the subcommand, extra flags).
WRAPPERS = {
    "az_blob": (["storage", "blob"], []),
    "az_lease": (["storage", "blob", "lease"], []),
}


def joined_lines(text: str) -> list[str]:
    out = []
    buf = ""
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].rstrip()
        if not line.strip():
            continue
        if buf:
            buf = buf + " " + line.strip()
        else:
            buf = line.strip()
        if buf.endswith("\\"):
            buf = buf[:-1].rstrip() + " "
            continue
        out.append(buf.strip())
        buf = ""
    if buf.strip():
        out.append(buf.strip())
    return out


def tokens(line: str) -> list[str]:
    # `id=$(az_lease ...)` hides the command inside one shell word.
    line = line.replace("$(", " ")
    found = []
    for tok in re.findall(r"'[^']*'|\"[^\"]*\"|\S+", line):
        if tok.startswith("-"):
            tok = tok.rstrip(";)")
        else:
            tok = tok.rstrip(";")
        if tok:
            found.append(tok)
    return found


def flags_of(args: list[str]) -> list[str]:
    found = []
    i = 0
    while i < len(args):
        arg = args[i]
        if arg.startswith("--") and arg != "--":
            name = arg.split("=", 1)[0]
            found.append(name)
            if "=" not in arg and i + 1 < len(args) and not args[i + 1].startswith("-"):
                i += 1
        elif re.fullmatch(r"-[A-Za-z]", arg):
            found.append(arg)
            if i + 1 < len(args) and not args[i + 1].startswith("-"):
                i += 1
        i += 1
    return found


def load_wrappers(lines: list[str]) -> dict[str, tuple[list[str], list[str]]]:
    wrappers = {name: (list(prefix), []) for name, (prefix, _) in WRAPPERS.items()}
    i = 0
    while i < len(lines):
        match = re.match(r"(az_blob|az_lease)\(\)\s*\{", lines[i])
        if not match:
            i += 1
            continue
        name = match.group(1)
        body = []
        i += 1
        while i < len(lines) and lines[i] != "}":
            body.append(lines[i])
            i += 1
        prefix, _ = wrappers[name]
        extra = []
        for line in body:
            if '"$@"' in line or "'$@'" in line:
                extra.extend(flags_of(tokens(line)))
        wrappers[name] = (prefix, extra)
    return wrappers


STOP = {"||", "&&", "|", ";", "(", ")", "{", "}"}
REDIRECT = re.compile(r"^[0-9]*[&<>]")


def is_redirect(tok: str) -> bool:
    return tok in {">", ">>", "<", ">&", "&>"} or bool(REDIRECT.match(tok))


def split_command(raw: list[str]) -> tuple[list[str], list[str]]:
    words = []
    flag_tokens = []
    started = False
    for arg in raw:
        if arg.startswith("-") or "=" in arg:
            started = True
        if started:
            flag_tokens.append(arg)
        else:
            words.append(arg)
    return words, flags_of(flag_tokens)


def invocations(lines: list[str], wrappers: dict[str, tuple[list[str], list[str]]]) -> list[tuple[tuple[str, ...], tuple[str, ...]]]:
    found = []
    for line in lines:
        if re.match(r"az_(blob|lease)\(\)", line):
            continue
        if '"$@"' in line or "'$@'" in line:
            continue
        parts = tokens(line)
        i = 0
        while i < len(parts):
            head = parts[i]
            prev = parts[i - 1] if i else ""
            if head not in wrappers and head != "az":
                i += 1
                continue
            # `command -v az` and `export -f az` are not invocations.
            if head == "az" and prev in {"-v", "-f", "command"}:
                i += 1
                continue
            raw = []
            i += 1
            while i < len(parts) and parts[i] not in STOP and not is_redirect(parts[i]):
                raw.append(parts[i])
                i += 1
            words, flags = split_command(raw)
            if not words or any(is_redirect(word) or ">" in word for word in words):
                continue
            if head in wrappers:
                prefix, extra = wrappers[head]
                cmd = tuple(prefix + words)
                flags = list(dict.fromkeys(extra + flags))
            else:
                cmd = tuple(words)
            found.append((cmd, tuple(flags)))
    return found


FLAG_TOKEN = re.compile(r"(?<![\w-])(--[A-Za-z0-9][A-Za-z0-9-]*|-[A-Za-z])(?![\w-])")

# az storage blob tag set comes from storage-blob-preview. Prod does
# not install that extension. Core mode is the prod CLI. Preview mode
# is the test CLI.
EXTENSION_CMDS = {("storage", "blob", "tag", "set")}


def flag_tokens(text: str) -> set[str]:
    return set(FLAG_TOKEN.findall(text))


def run_help(cmd: tuple[str, ...]) -> tuple[int, str]:
    proc = subprocess.run(
        ["az", *cmd, "--help"],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    return proc.returncode, proc.stdout


def help_text(cmd: tuple[str, ...]) -> str:
    code, text = run_help(cmd)
    if code != 0:
        sys.stderr.write(f"az {' '.join(cmd)} --help failed:\n{text}\n")
        sys.exit(1)
    return text


def preview_installed() -> bool:
    proc = subprocess.run(
        ["az", "extension", "show", "--name", "storage-blob-preview", "--query", "version", "-o", "tsv"],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    return proc.returncode == 0 and bool(proc.stdout.strip())


def self_test() -> None:
    """Reject substring matches. --nam is not --name. --blob is not --blob-name."""
    sample = """
    --name -n           : The blob name.
    --blob-name -b      : The blob.
    --output -o         : Output format.
    --query             : JMESPath query string.
    """
    accepted = flag_tokens(sample)
    for flag in ("--name", "-n", "--blob-name", "-b", "--output", "-o", "--query"):
        if flag not in accepted:
            sys.stderr.write(f"self-test: {flag} was not accepted\n")
            sys.exit(1)
    for flag in ("--nam", "--blob", "--bogus", "--n"):
        if flag in accepted:
            sys.stderr.write(f"self-test: {flag} was accepted\n")
            sys.exit(1)
    # The old matcher treated help text as a substring haystack.
    # --name contains --nam, and --blob-name contains --blob.
    if "--nam" not in sample or "--blob" not in sample:
        sys.stderr.write("self-test: fixture no longer hides a short flag inside a longer one\n")
        sys.exit(1)
    print("az flag self-test ok")


def main() -> None:
    mode = os.environ.get("AZ_FLAG_CHECK", "preview")
    if mode not in {"core", "preview"}:
        sys.stderr.write(f"AZ_FLAG_CHECK must be core or preview, got {mode}\n")
        sys.exit(1)
    if mode == "preview" and not preview_installed():
        sys.stderr.write("storage-blob-preview is not installed\n")
        sys.exit(1)
    grouped: dict[tuple[str, ...], set[str]] = {}
    wrappers = {name: (list(prefix), []) for name, (prefix, _) in WRAPPERS.items()}
    parsed = []
    for path in FILES:
        lines = joined_lines(path.read_text())
        parsed.append(lines)
        for name, (prefix, extra) in load_wrappers(lines).items():
            if extra:
                wrappers[name] = (prefix, extra)
    for lines in parsed:
        for cmd, flags in invocations(lines, wrappers):
            grouped.setdefault(cmd, set()).update(flags)
    if not grouped:
        sys.stderr.write("no az commands found\n")
        sys.exit(1)
    required = {
        ("keyvault", "secret", "show"): {"--vault-name", "--name", "--query", "-o"},
        ("storage", "blob", "upload"): {"--auth-mode", "--name", "--file"},
        ("storage", "blob", "exists"): {"--name", "--query"},
        ("storage", "blob", "list"): {"--prefix", "--query"},
        ("storage", "blob", "lease", "acquire"): {"--blob-name", "--lease-duration"},
        ("storage", "blob", "lease", "renew"): {"--blob-name", "--lease-id"},
        ("storage", "blob", "lease", "release"): {"--blob-name", "--lease-id"},
        ("extension", "add"): {"--name", "--version"},
        ("config", "set"): set(),
        ("logout",): set(),
        ("version",): set(),
    }
    if mode == "preview":
        required[("storage", "blob", "tag", "set")] = {"--name", "--tags"}
    failed = False
    if ("storage", "blob", "tag", "set") not in grouped:
        sys.stderr.write("did not find az storage blob tag set\n")
        failed = True
    for cmd, need in required.items():
        have = grouped.get(cmd)
        if have is None:
            sys.stderr.write(f"did not find az {' '.join(cmd)}\n")
            failed = True
            continue
        missing = need - have
        if missing:
            sys.stderr.write(f"az {' '.join(cmd)} parser missed {', '.join(sorted(missing))}\n")
            failed = True
    global_flags = flag_tokens(help_text(()))
    for cmd in sorted(grouped):
        if cmd in EXTENSION_CMDS:
            code, text = run_help(cmd)
            if mode == "core":
                if code == 0:
                    sys.stderr.write(f"az {' '.join(cmd)} is available without storage-blob-preview\n")
                    failed = True
                continue
            if code != 0:
                sys.stderr.write(f"az {' '.join(cmd)} --help failed:\n{text}\n")
                failed = True
                continue
            known = flag_tokens(text) | global_flags
        else:
            known = flag_tokens(help_text(cmd)) | global_flags
        for flag in sorted(grouped[cmd]):
            if flag in known:
                continue
            sys.stderr.write(f"az {' '.join(cmd)} does not accept {flag}\n")
            failed = True
    # The bug this script exists to catch.
    kv = help_text(("keyvault", "secret", "show"))
    if "--auth-mode" in flag_tokens(kv):
        sys.stderr.write("keyvault secret show gained --auth-mode; revisit the publisher\n")
        sys.exit(1)
    if ("keyvault", "secret", "show") in grouped and "--auth-mode" in grouped[("keyvault", "secret", "show")]:
        sys.stderr.write("publisher still passes --auth-mode to keyvault secret show\n")
        failed = True
    if failed:
        sys.exit(1)
    print(f"az flags ok ({mode}, {len(grouped)} commands)")


if __name__ == "__main__":
    os.environ.setdefault("AZURE_EXTENSION_USE_DYNAMIC_INSTALL", "no")
    if "--self-test" in sys.argv:
        self_test()
    else:
        main()
