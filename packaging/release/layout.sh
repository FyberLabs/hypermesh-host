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

# semver_less A B
# True when A is a strictly older X.Y.Z than B.
semver_less() {
  local a=$1 b=$2
  [[ "$a" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
  local a1=${BASH_REMATCH[1]} a2=${BASH_REMATCH[2]} a3=${BASH_REMATCH[3]}
  [[ "$b" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
  local b1=${BASH_REMATCH[1]} b2=${BASH_REMATCH[2]} b3=${BASH_REMATCH[3]}
  if [ "$((10#$a1))" -lt "$((10#$b1))" ]; then return 0; fi
  if [ "$((10#$a1))" -gt "$((10#$b1))" ]; then return 1; fi
  if [ "$((10#$a2))" -lt "$((10#$b2))" ]; then return 0; fi
  if [ "$((10#$a2))" -gt "$((10#$b2))" ]; then return 1; fi
  [ "$((10#$a3))" -lt "$((10#$b3))" ]
}

# prod_pointer_ok NEW_VERSION NEW_BUILD CURRENT_VERSION CURRENT_BUILD
# Empty current version and build means channels/prod.json is absent:
# the first publish is allowed. Otherwise the new build must be a
# greater integer and the new semver must be strictly newer. An old
# tag re-run gets a higher workflow run number; the semver check still
# refuses to move the pointer backwards.
prod_pointer_ok() {
  local new_ver=$1 new_build=$2 cur_ver=${3:-} cur_build=${4:-}
  if ! [[ "$new_build" =~ ^[1-9][0-9]*$ ]]; then
    echo "hypermesh release: build number must be a positive integer" >&2
    return 1
  fi
  if ! [[ "$new_ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "hypermesh release: version must be X.Y.Z" >&2
    return 1
  fi
  if [ -z "$cur_ver" ] && [ -z "$cur_build" ]; then
    return 0
  fi
  if ! [[ "$cur_build" =~ ^[0-9]+$ ]]; then
    echo "hypermesh release: current prod build is not an integer" >&2
    return 1
  fi
  if [ "$((10#$new_build))" -le "$((10#$cur_build))" ]; then
    echo "hypermesh release: refusing to move channels/prod.json backwards (build ${new_build} <= ${cur_build})" >&2
    return 1
  fi
  if ! semver_less "$cur_ver" "$new_ver"; then
    echo "hypermesh release: refusing to move channels/prod.json to a version that is not newer" >&2
    return 1
  fi
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
