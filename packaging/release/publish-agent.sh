#!/bin/bash
# Publish a signed hypermesh-agent build to GitHub Releases.
#
# Usage: publish-agent.sh prod|test
#
# prod publishes the tag in GITHUB_REF_NAME (vX.Y.Z) as a stable release.
# test replaces the rolling prerelease tag "test" (builds of main).
#
# Signing uses the GPG private key in HYPERMESH_RELEASE_GPG_PRIVATE_KEY.
# That secret is not created by this repo. Provision it, and
# HYPERMESH_RELEASE_GPG_PASSPHRASE when the key has one, on the GitHub
# Environment named "release" before the first run. The script refuses
# to upload anything until the checksum file is signed.
#
# The repo is private. Agents download with HYPERMESH_UPDATE_GITHUB_TOKEN.
# This script uses the Actions token (GH_TOKEN) and does not print it.
set -euo pipefail

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

command -v go >/dev/null
command -v gpg >/dev/null
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
else
  tag=test
  version=0.0.0
  prerelease=true
fi

root=$(cd "$(dirname "$0")/../.." && pwd)
dist=$(mktemp -d)
gnupg=""
cleanup() { rm -rf "$dist" ${gnupg:+"$gnupg"}; }
trap cleanup EXIT

ldflags="-s -w -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=${GITHUB_SHA} -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Version=${version}"
for arch in arm64 amd64; do
  (
    cd "$root"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags "$ldflags" \
      -o "${dist}/hypermesh-agent_${version}_linux_${arch}" \
      ./cmd/agent
  )
done

printf '{"channel":"%s","version":"%s","commit":"%s"}\n' \
  "$channel" "$version" "$GITHUB_SHA" > "${dist}/release.json"

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
gpg --batch --export --armor --output "${dist}/hypermesh-release.pub" "$fpr"
shred_gnupg=$gnupg
gnupg=""
rm -rf "$shred_gnupg"
unset GNUPGHOME

auth=(-H "Authorization: Bearer ${GH_TOKEN}" -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28")
api="https://api.github.com/repos/${GITHUB_REPOSITORY}"

if [ "$channel" = test ]; then
  code=$(curl -sS -o "${dist}/existing.json" -w '%{http_code}' "${auth[@]}" \
    "${api}/releases/tags/test" || true)
  if [ "$code" = 200 ]; then
    rel_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "${dist}/existing.json")
    curl -sS -X DELETE "${auth[@]}" "${api}/releases/${rel_id}" >/dev/null
    curl -sS -X DELETE "${auth[@]}" "${api}/git/refs/tags/test" >/dev/null || true
  fi
fi

notes="Signed hypermesh-agent ${version} (${GITHUB_SHA})."
if [ "$channel" = test ]; then
  notes="Rolling build of main ${GITHUB_SHA}. Not a prod release."
fi
payload=$(python3 -c 'import json,sys; print(json.dumps({"tag_name":sys.argv[1],"name":sys.argv[1],"body":sys.argv[2],"target_commitish":sys.argv[3],"draft":False,"prerelease":sys.argv[4]=="true"}))' \
  "$tag" "$notes" "$GITHUB_SHA" "$prerelease")
code=$(curl -sS -o "${dist}/created.json" -w '%{http_code}' "${auth[@]}" \
  -H "Content-Type: application/json" \
  -X POST --data "$payload" "${api}/releases")
if [ "$code" != 201 ]; then
  echo "hypermesh release: create ${tag} failed with HTTP ${code}" >&2
  exit 1
fi
rel_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "${dist}/created.json")

upload() {
  name=$1
  file=$2
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${GH_TOKEN}" \
    -H "Content-Type: application/octet-stream" \
    -H "Accept: application/vnd.github+json" \
    -X POST --data-binary @"${file}" \
    "https://uploads.github.com/repos/${GITHUB_REPOSITORY}/releases/${rel_id}/assets?name=${name}")
  if [ "$code" != 201 ]; then
    echo "hypermesh release: upload ${name} failed with HTTP ${code}" >&2
    exit 1
  fi
}

upload "hypermesh-agent_${version}_linux_arm64" "${dist}/hypermesh-agent_${version}_linux_arm64"
upload "hypermesh-agent_${version}_linux_amd64" "${dist}/hypermesh-agent_${version}_linux_amd64"
upload release.json "${dist}/release.json"
upload checksums.txt "${dist}/checksums.txt"
upload checksums.txt.sig "${dist}/checksums.txt.sig"
upload hypermesh-release.pub "${dist}/hypermesh-release.pub"

echo "hypermesh release: published ${tag} ${GITHUB_SHA}"
