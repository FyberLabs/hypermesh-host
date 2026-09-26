# Release-id helpers for publish-agent.sh.
# Tests source this file. It does not upload and does not read secrets.

# release_id_test TIMESTAMP SHA
# TIMESTAMP is UTC yyyymmddHHMMSS. SHA is the full 40-hex commit.
# The result is <timestamp>-<first 12 hex chars>. String order is time order.
release_id_test() {
  local ts=$1 sha=$2 hex
  if ! [[ "$ts" =~ ^[0-9]{14}$ ]]; then
    echo "hypermesh release: timestamp must be UTC yyyymmddHHMMSS" >&2
    return 1
  fi
  hex=$(printf '%s' "$sha" | tr 'A-F' 'a-f')
  if ! [[ "$hex" =~ ^[0-9a-f]{40}$ ]]; then
    echo "hypermesh release: commit must be 40 hex characters" >&2
    return 1
  fi
  printf '%s-%s\n' "$ts" "${hex:0:12}"
}

# release_id_prod VERSION
# VERSION is X.Y.Z with no leading v. That string is the prod directory name.
release_id_prod() {
  local version=$1
  if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "hypermesh release: prod release-id must be X.Y.Z" >&2
    return 1
  fi
  printf '%s\n' "$version"
}
