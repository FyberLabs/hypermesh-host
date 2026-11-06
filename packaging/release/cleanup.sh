#!/usr/bin/env bash
# Best-effort daemon/logout teardown; verified, bounded removal remains mandatory.
set +x
set -euo pipefail

safe_cleanup_path() {
  local target=$1 label=$2 resolved allowed=false root
  [ -n "$target" ] || return 0
  resolved=$(realpath -m -- "$target") || return 1
  for root in "${RUNNER_TEMP:-}" "${TMPDIR:-/tmp}"; do
    [ -n "$root" ] || continue
    root=$(realpath -m -- "$root") || return 1
    [ "$root" != / ] || continue
    case "$resolved" in "$root"/*) allowed=true ;; esac
  done
  if [ "$allowed" != true ]; then
    echo "hypermesh release: refusing unsafe $label cleanup path" >&2
    return 1
  fi
}

remove_sensitive_dir() {
  local target=$1 label=$2 attempt
  [ -n "$target" ] || return 0
  safe_cleanup_path "$target" "$label" || return 1
  for attempt in 1 2 3 4 5; do
    # az/gpg loggers can recreate entries while rm traverses the directory.
    timeout 10s rm -rf -- "$target" >/dev/null 2>&1 || true
    if [ ! -e "$target" ] && [ ! -L "$target" ]; then
      return 0
    fi
    if [ "$attempt" -lt 5 ]; then sleep 1; fi
  done
  echo "hypermesh release: $label cleanup did not finish" >&2
  return 1
}

status=0
if [ -n "${GNUPGHOME:-}" ]; then
  if safe_cleanup_path "$GNUPGHOME" 'signing material'; then
    timeout 20s gpgconf --kill all >/dev/null 2>&1 || true
    if [ -d "$GNUPGHOME" ] && command -v shred >/dev/null 2>&1; then
      timeout 20s find "$GNUPGHOME" -type f -exec shred -u {} + >/dev/null 2>&1 || true
    fi
    if ! remove_sensitive_dir "$GNUPGHOME" 'signing material'; then status=1; fi
  else
    status=1
  fi
fi
# Always attempt Azure cleanup, even when signing-directory removal failed.
if [ -n "${AZURE_CONFIG_DIR:-}" ]; then
  if safe_cleanup_path "$AZURE_CONFIG_DIR" 'Azure configuration'; then
    timeout 20s az logout >/dev/null 2>&1 || true
    if ! remove_sensitive_dir "$AZURE_CONFIG_DIR" 'Azure configuration'; then status=1; fi
  else
    status=1
  fi
fi
exit "$status"
