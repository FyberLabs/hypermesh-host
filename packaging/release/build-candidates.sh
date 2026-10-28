#!/usr/bin/env bash
# Build downloadable lab binaries without signing keys or channel publication.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=layout.sh
source "${script_dir}/layout.sh"
root=$(cd "${script_dir}/../.." && pwd)
dist=${1:?usage: build-candidates.sh NEW_OUTPUT_DIRECTORY}
if [ -e "$dist" ]; then
  echo "candidate output must be a new directory" >&2
  exit 1
fi
if [ -n "$(git -C "$root" status --porcelain --untracked-files=all)" ]; then
  echo "candidate builds require a clean checkout so the source commit is unambiguous" >&2
  exit 1
fi
commit=$(git -C "$root" rev-parse HEAD)
mkdir -p "$dist"
dist=$(cd "$dist" && pwd)
ldflags="-s -w -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Commit=${commit} -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Version=0.0.0 -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Build=0 -X github.com/FyberLabs/hypermesh-host/internal/buildinfo.Channel=test"
build_release_binaries "$root" "$dist" 0.0.0 "$ldflags"
# A lab bundle is not a signed installation release. Do not include bootstrap.
rm -- "$dist/bootstrap.py"
cat > "$dist/README.txt" <<NOTE
Lab test binaries from source commit ${commit}.
Targets: Linux ARM64 (cpu), Linux AMD64 (rocm,cpu), Darwin ARM64.
These unsigned CI artifacts have build 0. They do not advance a signed channel
or authorize guided installation, enrollment, hardware qualification or selling.
Verify SHA256SUMS after download. Use platforms and inspect for initial hardware
checks, or run the agent in an explicitly configured lab environment.
A signed test-channel release follows the normal reviewed main-branch publisher.
NOTE
(cd "$dist" && sha256sum hypermesh-* platforms.json README.txt > SHA256SUMS)
printf 'Lab binaries: %s\n' "$dist"
