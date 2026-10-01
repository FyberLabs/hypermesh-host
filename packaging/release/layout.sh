# shellcheck shell=bash
# Release helpers for publish-agent.sh.
# Tests source this file. It does not upload.
# Key Vault reads run only when load_signing_material is called.

# channel_for_ref REF
# Prints test for refs/heads/main and prod for refs/tags/v*.
# Any other ref fails, including a workflow_dispatch from another branch.
# This does not check that a tag commit is on origin/main.
channel_for_ref() {
  local ref=$1
  case "$ref" in
    refs/heads/main)
      printf '%s\n' test
      ;;
    refs/tags/v*)
      printf '%s\n' prod
      ;;
    *)
      echo "hypermesh release: refusing ${ref}" >&2
      echo "test publishes only from refs/heads/main; prod publishes only from refs/tags/v* that are on origin/main" >&2
      return 1
      ;;
  esac
}

# channel_artifact_name PREFIX NAME
# NAME is <prefix>/<one segment>/<one segment>, with no ".." segment.
channel_artifact_name() {
  local prefix=$1 name=$2 rid file
  case "$name" in
    "${prefix}"/*/*) ;;
    *) return 1 ;;
  esac
  case "$name" in
    *..*|*\\*) return 1 ;;
  esac
  file=${name#"${prefix}/"}
  rid=${file%%/*}
  file=${file#*/}
  if [ -z "$rid" ] || [ -z "$file" ] || [[ "$file" == */* ]]; then
    return 1
  fi
  case "$rid" in
    .*|*/*) return 1 ;;
  esac
  case "$file" in
    .*|*/*) return 1 ;;
  esac
}

# channel_write_allowed CHANNEL NAME
# Each publisher writes its own prefix and its own channel pointer.
# Both also write exactly locks/release-publish, and nothing else
# outside that prefix. Cross-channel indexes are not writes.
channel_write_allowed() {
  local channel=$1 name=$2
  case "$channel" in
    test|prod) ;;
    *) return 1 ;;
  esac
  if [ "$name" = "locks/release-publish" ]; then
    return 0
  fi
  case "$channel" in
    test)
      if [ "$name" = "channels/test.json" ]; then
        return 0
      fi
      channel_artifact_name test "$name"
      ;;
    prod)
      if [ "$name" = "channels/prod.json" ]; then
        return 0
      fi
      channel_artifact_name prod "$name"
      ;;
  esac
}

# publish_blob_allowed CHANNEL OP NAME
# OP is read, write, or tag.
# Test may read channels/prod.json. Prod may read channels/test.json.
# Those reads are not writes. Tag is test artifacts only, superseded
# by the caller. Prod has no tag operation.
publish_blob_allowed() {
  local channel=$1 op=$2 name=$3
  case "$op" in
    read)
      if [ "$name" = "locks/release-publish" ]; then
        case "$channel" in
          test|prod) return 0 ;;
          *) return 1 ;;
        esac
      fi
      case "$channel" in
        test)
          case "$name" in
            channels/test.json|channels/prod.json) return 0 ;;
          esac
          channel_artifact_name test "$name"
          ;;
        prod)
          case "$name" in
            channels/prod.json|channels/test.json) return 0 ;;
          esac
          channel_artifact_name prod "$name"
          ;;
        *) return 1 ;;
      esac
      ;;
    write)
      channel_write_allowed "$channel" "$name"
      ;;
    tag)
      [ "$channel" = test ] || return 1
      channel_artifact_name test "$name"
      ;;
    *) return 1 ;;
  esac
}

# publish_list_allowed CHANNEL PREFIX
# Only the test publisher lists, and only under test/. Prod does not list.
publish_list_allowed() {
  local channel=$1 prefix=$2
  [ "$channel" = test ] || return 1
  case "$prefix" in
    test/) return 0 ;;
    test/*)
      case "$prefix" in
        *..*|*\\*) return 1 ;;
        *) return 0 ;;
      esac
      ;;
    *) return 1 ;;
  esac
}

# enforce_publish_blob CHANNEL AZ_STORAGE_BLOB_ARGS...
# Refuses delete and any blob call outside publish_blob_allowed.
# Tag values must be exactly superseded=true.
enforce_publish_blob() {
  local channel=$1
  shift
  local cmd=${1:-}
  shift || true
  local op name="" prefix="" tags="" expect="" arg
  case "$cmd" in
    exists|download) op="read" ;;
    upload) op="write" ;;
    list) op="list" ;;
    tag)
      op="tag"
      if [ "${1:-}" = set ]; then
        shift
      fi
      ;;
    *)
      echo "hypermesh release: refusing blob ${cmd:-<empty>} on the ${channel} publisher" >&2
      return 1
      ;;
  esac
  for arg in "$@"; do
    if [ -n "$expect" ]; then
      case "$expect" in
        name) name=$arg ;;
        prefix) prefix=$arg ;;
        tags) tags=$arg ;;
      esac
      expect=""
      continue
    fi
    case "$arg" in
      --name|--blob-name) expect=name ;;
      --prefix) expect=prefix ;;
      --tags) expect=tags ;;
      --name=*) name=${arg#--name=} ;;
      --blob-name=*) name=${arg#--blob-name=} ;;
      --prefix=*) prefix=${arg#--prefix=} ;;
      --tags=*) tags=${arg#--tags=} ;;
    esac
  done
  if [ -n "$expect" ]; then
    echo "hypermesh release: blob ${cmd} is missing a value" >&2
    return 1
  fi
  if [ "$op" = list ]; then
    if ! publish_list_allowed "$channel" "$prefix"; then
      echo "hypermesh release: refusing to list ${prefix:-<empty>} on the ${channel} publisher" >&2
      return 1
    fi
    return 0
  fi
  if [ -z "$name" ]; then
    echo "hypermesh release: blob ${cmd} needs a name" >&2
    return 1
  fi
  if ! publish_blob_allowed "$channel" "$op" "$name"; then
    echo "hypermesh release: refusing ${op} of ${name} on the ${channel} publisher" >&2
    return 1
  fi
  if [ "$op" = tag ] && [ "$tags" != "superseded=true" ]; then
    echo "hypermesh release: refusing tag ${tags:-<empty>} on ${name}" >&2
    return 1
  fi
}

# enforce_publish_lease CHANNEL AZ_STORAGE_BLOB_LEASE_ARGS...
# Lease is a write of locks/release-publish. No other blob, and only
# acquire, renew, or release.
enforce_publish_lease() {
  local channel=$1
  shift
  local cmd=${1:-}
  shift || true
  local name="" expect="" arg
  case "$cmd" in
    acquire|renew|release) ;;
    *)
      echo "hypermesh release: refusing lease ${cmd:-<empty>}" >&2
      return 1
      ;;
  esac
  for arg in "$@"; do
    if [ -n "$expect" ]; then
      name=$arg
      expect=""
      continue
    fi
    case "$arg" in
      --blob-name) expect=name ;;
      --blob-name=*) name=${arg#--blob-name=} ;;
    esac
  done
  if [ "$name" != "locks/release-publish" ]; then
    echo "hypermesh release: refusing lease on ${name:-<empty>}" >&2
    return 1
  fi
  if ! publish_blob_allowed "$channel" write "$name"; then
    echo "hypermesh release: refusing lease on ${name} for the ${channel} publisher" >&2
    return 1
  fi
}

# require_channel_write CHANNEL NAME
require_channel_write() {
  local channel=$1 name=$2
  if ! channel_write_allowed "$channel" "$name"; then
    echo "hypermesh release: refusing to write ${name} on the ${channel} publisher" >&2
    return 1
  fi
}

# release_id_test BUILD SHA
# BUILD is the shared positive build number. SHA is the full 40-hex
# commit. The result is <build>-<first 12 hex chars>. That directory
# is immutable. Order is the integer build, not the string order of
# this id.
release_id_test() {
  local run=$1 sha=$2 hex
  if ! [[ "$run" =~ ^[1-9][0-9]*$ ]]; then
    echo "hypermesh release: run number must be a positive integer" >&2
    return 1
  fi
  hex=$(printf '%s' "$sha" | tr 'A-F' 'a-f')
  if ! [[ "$hex" =~ ^[0-9a-f]{40}$ ]]; then
    echo "hypermesh release: commit must be 40 hex characters" >&2
    return 1
  fi
  printf '%s-%s\n' "$run" "${hex:0:12}"
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

# prod_pointer_relation FILE RELEASE_ID BUILD COMMIT
# Prints match when the file already names this release_id, build, and
# commit. Prints mismatch when the release_id and build match but the
# commit does not. Prints other for every other document.
prod_pointer_relation() {
  python3 -c '
import json, sys
path, rid, build, commit = sys.argv[1:5]
try:
    d = json.load(open(path))
except Exception:
    print("other")
    raise SystemExit
b = d.get("build")
if isinstance(b, bool) or not isinstance(b, int):
    print("other")
    raise SystemExit
if str(d.get("release_id") or "") != rid or str(b) != str(build):
    print("other")
    raise SystemExit
got = str(d.get("commit") or "").lower()
print("match" if got == commit.lower() else "mismatch")
' "$1" "$2" "$3" "$4"
}

# test_build_behind BUILD
# 0 when channels/test.json is missing or names a strictly smaller
# build. 1 when that build is greater or equal. 2 when the read failed.
test_build_behind() {
  local build=$1 n
  if ! [[ "$build" =~ ^[1-9][0-9]*$ ]]; then
    echo "hypermesh release: build number must be a positive integer" >&2
    return 2
  fi
  if ! n=$(read_index_build test); then
    return 2
  fi
  n=${n//$'\n'/}
  if [ -z "$n" ]; then
    return 0
  fi
  if [ "$((10#$build))" -le "$((10#$n))" ]; then
    return 1
  fi
  return 0
}

# prod_publish_gate SKIP REUSE BUILD
# SKIP 1 means channels/prod.json already names this release. A re-run
# may repair a GitHub Release that only partly uploaded, even when
# channels/test.json has caught up. The "bump the version" check runs
# only when the pointer will be written (SKIP 0) and the signed build
# is being reused (REUSE 1).
prod_publish_gate() {
  local skip=$1 reuse=$2 build=$3 rc
  if [ "$skip" = 1 ]; then
    return 0
  fi
  if [ "$reuse" != 1 ]; then
    return 0
  fi
  if test_build_behind "$build"; then
    return 0
  else
    rc=$?
  fi
  if [ "$rc" -eq 1 ]; then
    echo "hypermesh release: bump the version" >&2
    return 1
  fi
  return "$rc"
}

# classify_github_asset LOCAL_SHA EXISTING_SHA
# EXISTING_SHA empty means the release has no asset with this name.
# Prints upload or skip. A different sha fails with bump the version.
classify_github_asset() {
  local local_sha=$1 existing=${2:-}
  local_sha=$(printf '%s' "$local_sha" | tr 'A-F' 'a-f')
  if [ -z "$existing" ]; then
    printf 'upload\n'
    return 0
  fi
  existing=$(printf '%s' "$existing" | tr 'A-F' 'a-f')
  existing=${existing#sha256:}
  if [ "$local_sha" = "$existing" ]; then
    printf 'skip\n'
    return 0
  fi
  echo "hypermesh release: bump the version" >&2
  return 1
}

# github_asset_digest JSON NAME
# Prints "sha <hex>", "id <asset id>" when the asset has no sha256
# digest, or "absent".
github_asset_digest() {
  python3 -c '
import json, sys
doc = json.load(open(sys.argv[1]))
name = sys.argv[2]
assets = doc if isinstance(doc, list) else []
for item in assets:
    if item.get("name") != name:
        continue
    digest = str(item.get("digest") or "")
    if digest.startswith("sha256:") and len(digest) > len("sha256:"):
        print("sha", digest.split(":", 1)[1])
        raise SystemExit
    print("id", item.get("id") if item.get("id") is not None else "")
    raise SystemExit
print("absent")
' "$1" "$2"
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

# next_build TEST_BUILD PROD_BUILD RUN_NUMBER
# Each argument is an integer, or empty when that index is missing.
# The result is one greater than the largest integer among them.
# Call this only while the release-publish concurrency group is held.
# An empty set of inputs fails.
next_build() {
  local max=0 seen=0 n
  for n in "$@"; do
    if [ -z "$n" ]; then
      continue
    fi
    if ! [[ "$n" =~ ^[0-9]+$ ]]; then
      echo "hypermesh release: build is not an integer" >&2
      return 1
    fi
    seen=1
    if [ "$((10#$n))" -gt "$max" ]; then
      max=$((10#$n))
    fi
  done
  if [ "$seen" -eq 0 ]; then
    echo "hypermesh release: no build to increment" >&2
    return 1
  fi
  printf '%s\n' "$((max + 1))"
}

# blob_exists NAME
# 0 when the blob exists, 1 when it is absent, 2 when the lookup failed
# or the answer was not exactly true or false. Only an explicit false
# is absence. A 403, a network error, or an empty answer is 2.
blob_exists() {
  local name=$1 exists
  if ! exists=$(az_blob exists --name "$name" --query exists --output tsv); then
    echo "hypermesh release: exists ${name} failed" >&2
    return 2
  fi
  if [ "$exists" = "true" ]; then
    return 0
  fi
  if [ "$exists" = "false" ]; then
    return 1
  fi
  echo "hypermesh release: exists ${name} was not true or false" >&2
  return 2
}

# read_index_build NAME
# Prints the build from channels/<name>.json. An explicit missing blob
# prints an empty line and returns 0. Any other lookup, download, or
# parse failure returns 1. Callers inside $(...) must use `|| exit 1`:
# a failure in this function is not a missing index.
read_index_build() {
  local name=$1 tmp build rc=0
  blob_exists "channels/${name}.json" || rc=$?
  if [ "$rc" -eq 1 ]; then
    printf '\n'
    return 0
  fi
  if [ "$rc" -ne 0 ]; then
    return 1
  fi
  tmp=$(mktemp)
  # stdout is the function return value; Azure download metadata is not.
  if ! az_blob download --name "channels/${name}.json" --file "$tmp" --overwrite true --only-show-errors > /dev/null; then
    rm -f "$tmp"
    echo "hypermesh release: download channels/${name}.json failed" >&2
    return 1
  fi
  if ! build=$(python3 -c '
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(1)
b = d.get("build")
if b is None:
    print("")
elif isinstance(b, bool) or not isinstance(b, int):
    sys.exit(1)
else:
    print(b)
' "$tmp"); then
    rm -f "$tmp"
    echo "hypermesh release: channels/${name}.json is not a channel index" >&2
    return 1
  fi
  rm -f "$tmp"
  if [ -n "$build" ] && ! [[ "$build" =~ ^[0-9]+$ ]]; then
    echo "hypermesh release: channels/${name}.json build is not an integer" >&2
    return 1
  fi
  printf '%s\n' "$build"
}

# require_builds_ahead BUILD
# Re-reads both channel pointers. BUILD must be strictly greater than
# each build that is present. A missing pointer is not a build. A
# failed read aborts.
require_builds_ahead() {
  local build=$1 name n rc
  if ! [[ "$build" =~ ^[1-9][0-9]*$ ]]; then
    echo "hypermesh release: build number must be a positive integer" >&2
    return 1
  fi
  for name in test prod; do
    if ! n=$(read_index_build "$name"); then
      return 1
    fi
    n=${n//$'\n'/}
    if [ -z "$n" ]; then
      continue
    fi
    if [ "$((10#$build))" -le "$((10#$n))" ]; then
      echo "hypermesh release: build ${build} is not greater than channels/${name}.json (${n})" >&2
      return 1
    fi
  done
}

# lease_id_clean ID
# True when ID can be an x-ms-lease-id header. One token: letters,
# digits, and hyphens. Pretty-printed JSON has newlines and quotes, and
# requests rejects it as an invalid header value.
lease_id_clean() {
  local id=$1
  local re='^[A-Za-z0-9-]+$'
  [[ "$id" =~ $re ]]
}

# lease_held_by_other FILE...
# Another publisher holds locks/release-publish. A header error is not
# this case, and retrying it will not succeed.
lease_held_by_other() {
  local file
  for file in "$@"; do
    if [ -f "$file" ] && grep -qi -e 'LeaseAlreadyPresent' -e 'already a lease' "$file"; then
      return 0
    fi
  done
  return 1
}

# read_lease_token FILE
# Prints one clean lease id. acquire returns that id as its own output.
# --query leaseId is null on a string, so it used to discard a lease
# that had already been taken. A JSON object is not an id.
read_lease_token() {
  local file=$1 raw id
  raw=$(tr -d '\r' <"$file") || return 1
  raw=${raw%$'\n'}
  if [ -z "$raw" ] || [[ "$raw" == *$'\n'* ]]; then
    return 1
  fi
  if [[ "$raw" == \"*\" ]]; then
    id=${raw#\"}
    id=${id%\"}
  else
    id=$raw
  fi
  if ! lease_id_clean "$id"; then
    return 1
  fi
  printf '%s\n' "$id"
}

# acquire_publish_lease
# Creates locks/release-publish when it is absent, then acquires a 60s lease.
# Another holder is a wait. Any other error fails immediately and prints
# the az stderr (and stdout, when that is where az wrote the error).
# Prints the lease id on stdout. Nothing else: upload's pretty-printed
# JSON must not be captured as the id. release would send it as
# x-ms-lease-id. Lease commands get only the clean token. There is no
# JSON metadata header.
# That blob is the one write and lease both publishers have outside
# their channel prefix. Nothing else outside the prefix is written.
acquire_publish_lease() {
  local attempts=${HYPERMESH_LEASE_ATTEMPTS:-120}
  local pause=${HYPERMESH_LEASE_PAUSE:-15}
  local i exists empty id outf errf rc
  if ! exists=$(az_blob exists --name "locks/release-publish" --query exists --output tsv); then
    echo "hypermesh release: lock blob lookup failed" >&2
    return 1
  fi
  if [ "$exists" = "false" ]; then
    empty=$(mktemp)
    : >"$empty"
    # --output none and the redirect keep the upload result off stdout.
    # That result is pretty-printed JSON. A failed acquire used to assign
    # it to lease_id, and release passed it through as a header.
    if ! az_blob upload --name "locks/release-publish" --file "$empty" --overwrite false --only-show-errors --output none >/dev/null; then
      rm -f "$empty"
      if ! exists=$(az_blob exists --name "locks/release-publish" --query exists --output tsv); then
        echo "hypermesh release: lock blob lookup failed" >&2
        return 1
      fi
      if [ "$exists" != "true" ]; then
        echo "hypermesh release: could not create locks/release-publish" >&2
        return 1
      fi
    else
      rm -f "$empty"
    fi
  elif [ "$exists" != "true" ]; then
    echo "hypermesh release: lock blob exists query was not true or false" >&2
    return 1
  fi
  outf=$(mktemp)
  errf=$(mktemp)
  i=0
  while [ "$i" -lt "$attempts" ]; do
    i=$((i + 1))
    : >"$outf"
    : >"$errf"
    rc=0
    az_lease acquire --blob-name "locks/release-publish" --lease-duration 60 --only-show-errors --output tsv >"$outf" 2>"$errf" || rc=$?
    if [ "$rc" -eq 0 ] && id=$(read_lease_token "$outf"); then
      rm -f "$outf" "$errf"
      printf '%s\n' "$id"
      return 0
    fi
    if lease_held_by_other "$errf" "$outf"; then
      if [ "$i" -ge "$attempts" ]; then
        break
      fi
      sleep "$pause"
      continue
    fi
    echo "hypermesh release: lock acquire failed" >&2
    cat "$errf" >&2
    if [ -s "$outf" ]; then
      cat "$outf" >&2
    fi
    rm -f "$outf" "$errf"
    return 1
  done
  rm -f "$outf" "$errf"
  echo "hypermesh release: timed out waiting for locks/release-publish" >&2
  return 1
}

# renew_publish_lease LEASE_ID
# Blocks, renewing until killed. Failures are ignored so the holder
# can release on the way out. The lease id is one clean token.
renew_publish_lease() {
  local id=$1 every=${HYPERMESH_LEASE_RENEW:-30}
  if ! lease_id_clean "$id"; then
    echo "hypermesh release: locks/release-publish lease id is not a clean token" >&2
    return 1
  fi
  while true; do
    sleep "$every"
    az_lease renew --blob-name "locks/release-publish" --lease-id "$id" --only-show-errors --output none || true
  done
}

# release_publish_lease LEASE_ID
# A value that is not a single token is not sent. Pretty-printed JSON
# becomes an illegal x-ms-lease-id header.
release_publish_lease() {
  local id=$1
  if [ -z "$id" ]; then
    return 0
  fi
  if ! lease_id_clean "$id"; then
    echo "hypermesh release: refusing to release locks/release-publish; lease id is not a clean token" >&2
    return 0
  fi
  az_lease release --blob-name "locks/release-publish" --lease-id "$id" --only-show-errors --output none || true
}

# confirm_publish_lease LEASE_ID
# Renews locks/release-publish immediately before the channel pointer
# is written. A failed renew means this publisher lost the lease.
confirm_publish_lease() {
  local id=$1
  if ! lease_id_clean "$id"; then
    echo "hypermesh release: locks/release-publish lease is not held" >&2
    return 1
  fi
  if ! az_lease renew --blob-name "locks/release-publish" --lease-id "$id" --only-show-errors --output none; then
    echo "hypermesh release: locks/release-publish lease was lost" >&2
    return 1
  fi
}

# gpg_signature_ok
# Reads gpg --status-fd text on stdin. GOODSIG and VALIDSIG are both
# required. EXPKEYSIG, REVKEYSIG, EXPSIG, BADSIG, and ERRSIG reject
# even when VALIDSIG is also present.
gpg_signature_ok() {
  python3 -c '
import sys
bad = {"EXPKEYSIG", "REVKEYSIG", "EXPSIG", "BADSIG", "ERRSIG"}
good = False
valid = False
for line in sys.stdin:
    parts = line.split()
    if len(parts) < 2 or parts[0] != "[GNUPG:]":
        continue
    tok = parts[1]
    if tok in bad:
        sys.exit(1)
    if tok == "GOODSIG":
        good = True
    if tok == "VALIDSIG":
        valid = True
sys.exit(0 if good and valid else 1)
'
}

# gpg_keys_current
# Every public key in GNUPGHOME must be unexpired and unrevoked.
gpg_keys_current() {
  gpg --batch --with-colons --list-keys | python3 -c '
import sys, time
now = int(time.time())
cur = None
seen = False
bad_validity = set("eridn")
for line in sys.stdin:
    p = line.rstrip("\n").split(":")
    if not p:
        continue
    if p[0] in ("pub", "sub"):
        cur = p
        continue
    if p[0] != "fpr" or cur is None:
        continue
    validity = cur[1] if len(cur) > 1 else ""
    exp = cur[6] if len(cur) > 6 else ""
    if validity in bad_validity:
        sys.exit(1)
    if exp.isdigit() and int(exp) <= now:
        sys.exit(1)
    seen = True
    cur = None
sys.exit(0 if seen else 1)
'
}

# verify_detached CHECKSUMS SIG
# GNUPGHOME holds the public key. Does not create a signature.
verify_detached() {
  local sum=$1 sig=$2 status
  status=$(gpg --batch --status-fd 1 --verify "$sig" "$sum" 2>/dev/null || true)
  if ! printf '%s\n' "$status" | gpg_signature_ok; then
    echo "hypermesh release: stored checksum signature was rejected" >&2
    return 1
  fi
  if ! gpg_keys_current; then
    echo "hypermesh release: signing key is expired or revoked" >&2
    return 1
  fi
}

# manifest_sha256 CHECKSUMS NAME
manifest_sha256() {
  python3 -c '
import sys
name = sys.argv[2]
found = ""
for line in open(sys.argv[1]):
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    parts = line.split()
    if len(parts) != 2 or parts[1] != name:
        continue
    if found and found != parts[0]:
        sys.exit(2)
    found = parts[0]
if not found:
    sys.exit(1)
print(found)
' "$1" "$2"
}

# local_matches_manifest CHECKSUMS LOCAL NAME
# A local file whose sha256 is not the signed line fails with
# "bump the version".
local_matches_manifest() {
  local sums=$1 localfile=$2 name=$3 want got
  if ! want=$(manifest_sha256 "$sums" "$name"); then
    echo "hypermesh release: bump the version" >&2
    return 1
  fi
  got=$(sha256sum "$localfile" | awk '{print $1}')
  if [ "$got" != "$want" ]; then
    echo "hypermesh release: bump the version" >&2
    return 1
  fi
}

# classify_prod_reuse RELEASE_ID DIST
# Downloads the stored prod release.json, checksums.txt, and
# checksums.txt.sig into DIST, verifies that signature, and compares
# each local file named by the checksums. Prints "upload NAME" or
# "keep NAME". Does not detach-sign. A hash mismatch is
# "bump the version".
classify_prod_reuse() {
  local release_id=$1 dist=$2 name blob rc
  for name in release.json checksums.txt checksums.txt.sig; do
    if ! az_blob download --name "prod/${release_id}/${name}" --file "${dist}/${name}" --overwrite true --only-show-errors; then
      echo "hypermesh release: download prod/${release_id}/${name} failed" >&2
      return 1
    fi
  done
  verify_detached "${dist}/checksums.txt" "${dist}/checksums.txt.sig" || return 1
  while IFS= read -r name; do
    [ -z "$name" ] && continue
    if [ ! -f "${dist}/${name}" ]; then
      echo "hypermesh release: bump the version" >&2
      return 1
    fi
    local_matches_manifest "${dist}/checksums.txt" "${dist}/${name}" "$name" || return 1
    blob="prod/${release_id}/${name}"
    if blob_exists "$blob"; then
      printf 'keep %s\n' "$name"
    else
      rc=$?
      if [ "$rc" -ne 1 ]; then
        return 1
      fi
      printf 'upload %s\n' "$name"
    fi
  done < <(python3 -c '
import sys
for line in open(sys.argv[1]):
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    parts = line.split()
    if len(parts) == 2:
        print(parts[1])
' "${dist}/checksums.txt")
  printf 'keep %s\n' checksums.txt
  printf 'keep %s\n' checksums.txt.sig
}

# tag_superseded_test CURRENT_RELEASE_ID [POINTER_RELEASE_ID]
# Sets blob index tag superseded=true on test/ objects from releases
# older than the newest 20. Never tags CURRENT_RELEASE_ID, the release
# channels/test.json still names, prod/, or channels/. Call this before
# the channel pointer is written, so a tag failure leaves that pointer
# where it is. A failed list or tag fails the publish.
tag_superseded_test() {
  local current=$1 also=${2:-} names tagged name
  if ! names=$(az_blob list --prefix "test/" --query "[].name" --num-results "*" --output tsv --only-show-errors); then
    echo "hypermesh release: listing test/ failed" >&2
    return 1
  fi
  if ! tagged=$(printf '%s\n' "$names" | python3 -c '
import sys
current = sys.argv[1]
names = [ln.strip() for ln in sys.stdin if ln.strip()]
groups = {}
for name in names:
    if name.startswith("prod/") or name.startswith("channels/"):
        sys.stderr.write("hypermesh release: refusing to tag %s\n" % name)
        sys.exit(1)
    if not name.startswith("test/"):
        continue
    parts = name.split("/")
    if len(parts) < 3 or not parts[1] or not parts[2]:
        continue
    rid = parts[1]
    build, sep, rest = rid.partition("-")
    if not sep or not build.isdigit() or len(rest) != 12 or not all(c in "0123456789abcdef" for c in rest):
        continue
    groups.setdefault(rid, int(build))
ordered = sorted(groups.items(), key=lambda item: item[1], reverse=True)
keep = {rid for rid, _build in ordered[:20]}
also = sys.argv[2] if len(sys.argv) > 2 else ""
if current:
    keep.add(current)
if also:
    keep.add(also)
seen = set()
for name in names:
    parts = name.split("/")
    if len(parts) < 3 or not name.startswith("test/"):
        continue
    rid = parts[1]
    if rid in keep or rid not in groups:
        continue
    if name in seen:
        continue
    seen.add(name)
    print(name)
' "$current" "$also"); then
    return 1
  fi
  while IFS= read -r name; do
    [ -z "$name" ] && continue
    if ! channel_artifact_name test "$name"; then
      echo "hypermesh release: refusing to tag ${name}" >&2
      return 1
    fi
    if ! az_blob tag set --name "$name" --tags "superseded=true"; then
      echo "hypermesh release: tagging ${name} failed" >&2
      return 1
    fi
  done <<< "$tagged"
}

# load_signing_material VAULT KEY_SECRET PASS_SECRET
# After azure/login, reads the armored signing key and the passphrase
# from Key Vault into a 0700 GNUPGHOME. Does not print either value.
# The passphrase is masked in the Actions log. xtrace stays off.
# Defaults from infra #143, overridable by repository variables:
# vault kv-fyber-hmhost, secret hypermesh-host-release-gpg-private-key,
# secret hypermesh-host-release-gpg-passphrase.
# Sets GNUPGHOME and signing_fpr in the caller. Do not run this in $().
load_signing_material() {
  local vault=$1 key_secret=$2 pass_secret=$3
  local home key_file pass_file raw fpr
  set +x
  umask 077
  if [ -z "$vault" ] || [ -z "$key_secret" ] || [ -z "$pass_secret" ]; then
    echo "hypermesh release: Key Vault name and secret names are required" >&2
    return 1
  fi
  case "$vault" in
    *[!A-Za-z0-9-]*) echo "hypermesh release: Key Vault name is not a vault name" >&2; return 1 ;;
  esac
  case "$key_secret" in
    *[!A-Za-z0-9-]*) echo "hypermesh release: signing key secret name is not a secret name" >&2; return 1 ;;
  esac
  case "$pass_secret" in
    *[!A-Za-z0-9-]*) echo "hypermesh release: passphrase secret name is not a secret name" >&2; return 1 ;;
  esac
  home=$(mktemp -d)
  chmod 700 "$home"
  export GNUPGHOME="$home"
  if [ -n "${GITHUB_ENV:-}" ]; then
    printf 'GNUPGHOME=%s\n' "$home" >> "$GITHUB_ENV"
  fi
  key_file="${home}/key.asc"
  pass_file="${home}/pass"
  # keyvault secret show has no --auth-mode. azure-cli 2.90 rejects it.
  if ! az keyvault secret show --vault-name "$vault" --name "$key_secret" --query value -o tsv > "$key_file"; then
    echo "hypermesh release: reading the signing key from Key Vault failed" >&2
    return 1
  fi
  chmod 600 "$key_file"
  if ! grep -q '[^[:space:]]' "$key_file"; then
    echo "hypermesh release: signing key secret is empty" >&2
    return 1
  fi
  # Infra omits this optional secret for an unprotected signing key.
  # Capture diagnostics privately; never turn access/network errors into an
  # empty passphrase. GPG still must successfully sign and verify artifacts.
  if ! az keyvault secret show --vault-name "$vault" --name "$pass_secret" --query value -o tsv > "$pass_file" 2> "${home}/pass-error"; then
    if grep -q '^ERROR: (SecretNotFound)' "${home}/pass-error"; then
      : > "$pass_file"
      echo "hypermesh release: optional signing passphrase absent; using an empty passphrase" >&2
    else
      echo "hypermesh release: reading the signing passphrase from Key Vault failed (diagnostics suppressed)" >&2
      return 1
    fi
  fi
  rm -f "${home}/pass-error"
  # tsv appends one newline. The passphrase file must not keep it.
  raw=$(<"$pass_file")
  printf '%s' "$raw" > "$pass_file"
  chmod 600 "$pass_file"
  if [ -n "$raw" ]; then
    printf '%s\n' "::add-mask::${raw}" >&2
  fi
  unset raw
  if ! gpg --batch --pinentry-mode loopback --passphrase-file "$pass_file" --import "$key_file"; then
    echo "hypermesh release: importing the signing key failed" >&2
    return 1
  fi
  fpr=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')
  if [ -z "$fpr" ]; then
    echo "hypermesh release: imported key has no fingerprint" >&2
    return 1
  fi
  # publish-agent.sh reads this in the same shell. A command substitution
  # would drop GNUPGHOME.
  # shellcheck disable=SC2034
  signing_fpr=$fpr
}

# wipe_gnupg_home [DIR]
# Shreds files in DIR when shred exists, removes the directory, and
# kills gpg-agent. An empty DIR is left alone. The agent is stopped
# once before the shred, while its socket still exists, and again
# after the directory is gone.
wipe_gnupg_home() {
  local home=${1:-${GNUPGHOME:-}}
  set +x
  if [ -z "$home" ]; then
    return 0
  fi
  if command -v gpgconf >/dev/null 2>&1; then
    GNUPGHOME="$home" gpgconf --kill all || true
  fi
  if [ -d "$home" ]; then
    if command -v shred >/dev/null 2>&1; then
      find "$home" -type f -exec shred -u {} + || true
    fi
    rm -rf "$home"
  fi
  if command -v gpgconf >/dev/null 2>&1; then
    GNUPGHOME="$home" gpgconf --kill all || true
  fi
  if [ "${GNUPGHOME:-}" = "$home" ]; then
    unset GNUPGHOME
  fi
}
