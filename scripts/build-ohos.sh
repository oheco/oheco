#!/bin/sh
set -eu
root=$(CDPATH= cd "$(dirname "$0")/.." && pwd -P)
version=${OHECO_VERSION:-0.13.0}
build_dir=${OHECO_BUILD_DIR:-$root/build}
go_bin=${OHECO_GO:-$root/../go/bin/go}
if ! command -v binary-sign-tool >/dev/null 2>&1; then
  echo 'binary-sign-tool is missing; check the LLVM tool directory in PATH.' >&2
  exit 1
fi
if [ "$("$go_bin" env GOHOSTOS)" != ohos ]; then
  echo 'Run this script on the HarmonyOS host with the native OHOS Go toolchain.' >&2
  exit 1
fi
cd "$root"
mkdir -p "${build_dir:?}"
build_dir=$(CDPATH= cd "${build_dir:?}" && pwd -P)
printf 'Build output directory: %s\n' "$build_dir"
CGO_ENABLED=0 GOTOOLCHAIN=local "$go_bin" build -trimpath \
  -ldflags "-s -w -X main.version=$version" -o "${build_dir:?}/oo.new" ./cmd/oo
# The OHOS Go toolchain signs the final executable through binary-sign-tool.
OHECO_NO_AUTO_UPDATE=1 "${build_dir:?}/oo.new" --version
mv "${build_dir:?}/oo.new" "${build_dir:?}/oo"
