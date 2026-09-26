#!/bin/bash
# Publish a signed hypermesh-agent build.
#
# Usage: publish-agent.sh prod|test
#
# Blob layout (infra owns the account and the 20-day test/ lifecycle):
#
#   channels/test.json
#   channels/prod.json
#   test/<release-id>/...
#   prod/<release-id>/...
#
# test release-id is UTC yyyymmddHHMMSS plus the first 12 hex chars of
# the commit. prod release-id is X.Y.Z. A release-id directory is not
# overwritten. The channel manifest is uploaded only after every
# artifact in that directory has been uploaded and sha256-checked.
#
# The manifest upload is one Put Blob of a few hundred bytes. Readers
# see the previous document or the new one, never a partial file.
# A crash before that upload leaves the channel on the last good build.
#
# Account and container names come from AZURE_STORAGE_ACCOUNT and
# AZURE_STORAGE_CONTAINER. This file does not name a real account.
#
# Login is the Azure CLI session from azure/login (GitHub OIDC).
# This script does not take a storage account key.
#
# Build number is GITHUB_RUN_NUMBER. GitHub increases it by one for
# each run of that workflow file. The test workflow and the prod
# workflow do not share a counter. The number is written into the
# signed release.json, the channel manifest, and the binary
# (buildinfo.Build). The host refuses a candidate whose build is not
# greater than its own. Before channels/prod.json is replaced, this
# script reads the current pointer and refuses a build or a semver
# that is not strictly newer.
#
# A GitHub Release is also created so a person can download the same
# bytes. Hosts do not read GitHub. They ask the control plane.
#
# Signing uses HYPERMESH_RELEASE_GPG_PRIVATE_KEY on the GitHub
# Environment named "release". The script refuses to upload when that
# key is empty. HYPERMESH_RELEASE_GPG_PASSPHRASE is required only when
# the key has one.
set -euo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=layout.sh
source "${script_dir}/layout.sh"

channel=${1:?usage: publish-agent.sh prod|test}
case "$channel" in
  prod|test) ;;
  *) echo "hypermesh release: channel must be prod or test" >&2; exit 1 ;;
esac

: "${GITHUB_SHA:?}"
: "${GITHUB_REPOSITORY:?}"
: "${GH_TOKEN:?}"
if [ -z "${HYPERMESH_RELEASE_GPG_PRIVATE_KEY:-}" ]; then
  echo "hypermesh release: HYPERMESH_RELEASE_GPG_PRIVATE_KEY is empty" >&2
  echo "Provision it on the GitHub Environment named release (repo admin)." >&2
  exit 1
fi
if [ -z "${AZURE_STORAGE_ACCOUNT:-}" ] || [ -z "${AZURE_STORAGE_CONTAINER:-}" ]; then
  echo "hypermesh release: AZURE_STORAGE_ACCOUNT and AZURE_STORAGE_CONTAINER are required" >&2
  echo "Set them on the GitHub Environment named release." >&2
  exit 1
fi
case "$AZURE_STORAGE_ACCOUNT" in
  *[!a-z0-9]*) echo "hypermesh release: AZURE_STORAGE_ACCOUNT is not a storage account name" >&2; exit 1 ;;
esac
case "$AZURE_STORAGE_CONTAINER" in
  *[!a-z0-9-]*) echo "hypermesh release: AZURE_STORAGE_CONTAINER is not a container name" >&2; exit 1 ;;
esac

# Monotonic per workflow file, not across test and prod.
build=${GITHUB_RUN_NUMBER:-}
if ! [[ "$build" =~ ^[1-9][0-9]*$ ]]; then
  echo "hypermesh release: GITHUB_RUN_NUMBER must be a positive integer" >&2
  exit 1
fi

command -v go >/dev/null
command -v gpg >/dev/null
command -v az >/dev/null
command -v curl >/dev/null
command -v python3 >/dev/null
command -v sha256sum >/dev/null

if [ "$channel" = prod ]; then
  tag=${GITHUB_REF_NAME:?}
  version=${tag#v}
  prerelease=false
  if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "hypermesh release: tag must be vX.Y.Z" >&2
    exit 1
  fi
  release_id=$(release_id_prod "$version")
else
  tag=test
  version=0.0.0
  prerelease=true
  ts=$(date -u +%Y%m%d%H%M%S)
  release_id=$(release_id_test "$ts" "$GITHUB_SHA")
fi
case "$release_id" in
  *[/\\]*|..*) echo "hypermesh release: release-id is not a single path segment" >&2; exit 1 ;;
esac

root=$(cd "${script_dir}/../.." && pwd)
dist=$(mktemp -d)
gnupg=""
cleanup() { rm -rf "$dist" ${gnupg:+"$gnupg"}; }
trap cleanup EXIT

ldflags="-s -w -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=${GITHUB_SHA} -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Version=${version} -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Build=${build}"
for arch in arm64 amd64; do
  (
    cd "$root"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags "$ldflags" \
      -o "${dist}/hypermesh-agent_${version}_linux_${arch}" \
      ./cmd/agent
  )
done

python3 - "$channel" "$version" "$GITHUB_SHA" "$build" "$release_id" "${dist}/release.json" <<'PY'
import json, sys
channel, version, commit, build, release_id, out = sys.argv[1:]
doc = {
    "channel": channel,
    "version": version,
    "commit": commit,
    "build": int(build),
    "release_id": release_id,
}
with open(out, "w", encoding="utf-8") as fh:
    json.dump(doc, fh, separators=(",", ":"))
    fh.write("\n")
PY

(
  cd "$dist"
  sha256sum \
    "hypermesh-agent_${version}_linux_arm64" \
    "hypermesh-agent_${version}_linux_amd64" \
    release.json > checksums.txt
)

umask 077
gnupg=$(mktemp -d)
export GNUPGHOME="$gnupg"
mkdir -p "$GNUPGHOME"
chmod 700 "$GNUPGHOME"
printf '%s\n' "$HYPERMESH_RELEASE_GPG_PRIVATE_KEY" > "${gnupg}/key.asc"
gpg --batch --import "${gnupg}/key.asc"
fpr=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')
if [ -z "$fpr" ]; then
  echo "hypermesh release: imported key has no fingerprint" >&2
  exit 1
fi
printf '%s' "${HYPERMESH_RELEASE_GPG_PASSPHRASE:-}" > "${gnupg}/pass"
gpg --batch --pinentry-mode loopback --passphrase-file "${gnupg}/pass" \
  --local-user "$fpr" \
  --output "${dist}/checksums.txt.sig" \
  --detach-sign "${dist}/checksums.txt"
gpg --batch --pinentry-mode loopback --passphrase-file "${gnupg}/pass" \
  --local-user "$fpr" --armor \
  --output "${dist}/checksums.txt.asc" \
  --detach-sign "${dist}/checksums.txt"
gpg --batch --export --armor --output "${dist}/hypermesh-release.pub" "$fpr"
shred_gnupg=$gnupg
gnupg=""
rm -rf "$shred_gnupg"
unset GNUPGHOME
unset HYPERMESH_RELEASE_GPG_PRIVATE_KEY HYPERMESH_RELEASE_GPG_PASSPHRASE

artifact_names=(
  "hypermesh-agent_${version}_linux_arm64"
  "hypermesh-agent_${version}_linux_amd64"
  release.json
  checksums.txt
  checksums.txt.sig
  checksums.txt.asc
  hypermesh-release.pub
)

az_blob() {
  az storage blob "$@" \
    --auth-mode login \
    --account-name "$AZURE_STORAGE_ACCOUNT" \
    --container-name "$AZURE_STORAGE_CONTAINER"
}

# Leave an existing object in place. verify_uploaded rejects it when
# the bytes differ, and the channel manifest is not moved.
upload_new() {
  local name=$1 file=$2 exists
  exists=$(az_blob exists --name "$name" --query exists --output tsv)
  if [ "$exists" = "true" ]; then
    echo "hypermesh release: ${name} already stored; leaving those bytes in place" >&2
    return 0
  fi
  az_blob upload --name "$name" --file "$file" --overwrite false --only-show-errors
}

verify_uploaded() {
  local name=$1 file=$2 tmp got want
  tmp=$(mktemp)
  # --overwrite replaces the local temp file, not the blob.
  az_blob download --name "$name" --file "$tmp" --overwrite true --only-show-errors
  got=$(sha256sum "$tmp" | awk '{print $1}')
  want=$(sha256sum "$file" | awk '{print $1}')
  rm -f "$tmp"
  if [ "$got" != "$want" ]; then
    echo "hypermesh release: ${name} bytes do not match the signed build" >&2
    exit 1
  fi
}

write_channel_manifest() {
  python3 - "$channel" "$release_id" "$version" "$GITHUB_SHA" "$build" "$dist" "${dist}/channel.json" "${artifact_names[@]}" <<'PY'
import hashlib, json, os, sys
channel, release_id, version, commit, build, dist, out = sys.argv[1:8]
arts = []
for name in sys.argv[8:]:
    data = open(os.path.join(dist, name), "rb").read()
    arts.append({
        "name": name,
        "sha256": hashlib.sha256(data).hexdigest(),
        "size": len(data),
        "path": "%s/%s/%s" % (channel, release_id, name),
    })
doc = {
    "channel": channel,
    "release_id": release_id,
    "version": version,
    "commit": commit,
    "build": int(build),
    "artifacts": arts,
}
with open(out, "w", encoding="utf-8") as fh:
    json.dump(doc, fh, separators=(",", ":"))
    fh.write("\n")
PY
}

# Single Put Blob of channels/<channel>.json. The file is far under the
# CLI single-shot limit, so the write is atomic: the previous manifest
# stays visible until this object is committed. Not under test/, so the
# lifecycle rule that deletes test/ blobs does not remove the pointer.
upload_channel_manifest() {
  local name=$1 file=$2
  az_blob upload --name "$name" --file "$file" --overwrite true --content-type application/json --only-show-errors
}

# Read channels/prod.json before any pointer write. A missing blob is
# the first prod publish. build and semver both have to move forward,
# so a re-run of an older tag cannot win on a higher run number.
if [ "$channel" = prod ]; then
  # publish-order: prod-pointer
  cur_ver=""
  cur_build=""
  exists=$(az_blob exists --name "channels/prod.json" --query exists --output tsv)
  if [ "$exists" = "true" ]; then
    cur_file=$(mktemp)
    az_blob download --name "channels/prod.json" --file "$cur_file" --overwrite true --only-show-errors
    read -r cur_ver cur_build < <(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("version") or "", d.get("build") if d.get("build") is not None else "")' "$cur_file")
    rm -f "$cur_file"
  fi
  prod_pointer_ok "$version" "$build" "$cur_ver" "$cur_build"
fi

# publish-order: artifacts
for name in "${artifact_names[@]}"; do
  upload_new "${channel}/${release_id}/${name}" "${dist}/${name}"
done
# publish-order: verify
for name in "${artifact_names[@]}"; do
  verify_uploaded "${channel}/${release_id}/${name}" "${dist}/${name}"
done
write_channel_manifest
# publish-order: channel-manifest
upload_channel_manifest "channels/${channel}.json" "${dist}/channel.json"

auth=(-H "Authorization: Bearer ${GH_TOKEN}" -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28")
api="https://api.github.com/repos/${GITHUB_REPOSITORY}"

if [ "$channel" = test ]; then
  code=$(curl -sS -o "${dist}/existing.json" -w '%{http_code}' "${auth[@]}" \
    "${api}/releases/tags/test" || true)
  if [ "$code" = 200 ]; then
    gh_rel=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "${dist}/existing.json")
    curl -sS -X DELETE "${auth[@]}" "${api}/releases/${gh_rel}" >/dev/null
    curl -sS -X DELETE "${auth[@]}" "${api}/git/refs/tags/test" >/dev/null || true
  fi
fi

notes="Signed hypermesh-agent ${release_id} (${GITHUB_SHA}). Hosts install from the control plane."
if [ "$channel" = test ]; then
  notes="Test build ${release_id} of ${GITHUB_SHA}. Not a prod release. Hosts install from the control plane."
fi
payload=$(python3 -c 'import json,sys; print(json.dumps({"tag_name":sys.argv[1],"name":sys.argv[2],"body":sys.argv[3],"target_commitish":sys.argv[4],"draft":False,"prerelease":sys.argv[5]=="true"}))' \
  "$tag" "$release_id" "$notes" "$GITHUB_SHA" "$prerelease")
code=$(curl -sS -o "${dist}/created.json" -w '%{http_code}' "${auth[@]}" \
  -H "Content-Type: application/json" \
  -X POST --data "$payload" "${api}/releases")
if [ "$code" != 201 ]; then
  echo "hypermesh release: create ${tag} failed with HTTP ${code}" >&2
  exit 1
fi
gh_rel=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "${dist}/created.json")

upload_github() {
  name=$1
  file=$2
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${GH_TOKEN}" \
    -H "Content-Type: application/octet-stream" \
    -H "Accept: application/vnd.github+json" \
    -X POST --data-binary @"${file}" \
    "https://uploads.github.com/repos/${GITHUB_REPOSITORY}/releases/${gh_rel}/assets?name=${name}")
  if [ "$code" != 201 ]; then
    echo "hypermesh release: upload ${name} failed with HTTP ${code}" >&2
    exit 1
  fi
}

upload_github "hypermesh-agent_${version}_linux_arm64" "${dist}/hypermesh-agent_${version}_linux_arm64"
upload_github "hypermesh-agent_${version}_linux_amd64" "${dist}/hypermesh-agent_${version}_linux_amd64"
upload_github release.json "${dist}/release.json"
upload_github checksums.txt "${dist}/checksums.txt"
upload_github checksums.txt.sig "${dist}/checksums.txt.sig"
upload_github checksums.txt.asc "${dist}/checksums.txt.asc"
upload_github hypermesh-release.pub "${dist}/hypermesh-release.pub"

echo "hypermesh release: published ${channel} ${release_id} ${GITHUB_SHA}"
