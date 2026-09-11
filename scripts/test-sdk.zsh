#!/usr/bin/zsh
# Run on the HarmonyOS host after building oo and the sibling package index.
# The original SDK ZIPs are verified through oo's download cache before install.
set -eu
setopt PIPE_FAIL
sdk_test_source=${OHECO_SDK_SOURCE:-$HOME/dev/sdk/ohos-sdk/ohos}
sdk_test_repo=${0:A:h:h}
# Default to the same filesystem as ~/.oheco, including its case-folding rules.
sdk_test_dir=$(mktemp -d "${OHECO_SDK_TEST_PARENT:-$HOME}/.oheco-sdk-test.XXXXXXXX")
trap 'sdk_test_result=$?; if [[ ${OHECO_KEEP_TEST_ROOT:-0} = 1 ]]; then print -r -- "SDK test root: $sdk_test_dir (exit $sdk_test_result)"; else rm -rf -- "$sdk_test_dir"; fi' EXIT
export OHECO_ROOT="$sdk_test_dir/root with spaces"
sdk_test_oo="$sdk_test_repo/build/oo"
sdk_test_version=26.0.0.35-Beta
mkdir -p "$OHECO_ROOT/index" "$OHECO_ROOT/cache/downloads"
cp "$sdk_test_repo/../oheco-packages/public/index/v2/index.json" "$OHECO_ROOT/index/index.json"
typeset -A sdk_test_hashes
sdk_test_hashes=(
  native 07baa4ab8fa2e240c0675560678beb452c679ef52fa7f6a302f362d9821c04cc
  toolchains 79ae088481f91c32c4f2f8e0feeddad75b6dba3b0dfc208684da86f5d0d02b34
  ets e30cc26a9d1a9884bed3ae52b1551efae9c1f5752ceadc27bf3c71d953ebff5f
  js c84345c0269c2546e0eb4968de7349a80b9d70a16f20f6420e307806448e875e
  previewer 3735dcb58aa4c0eec404d123da9d5e02937e7f58aa2be53e1ed8dc69bd41f395
)
for sdk_test_component in native toolchains ets js previewer; do
  sdk_test_zip="$sdk_test_source/$sdk_test_component-ohos-arm64-$sdk_test_version.zip"
  [[ -f $sdk_test_zip ]] || sdk_test_zip="$sdk_test_source/$sdk_test_component-ohos-x64-$sdk_test_version.zip"
  ln -s "$sdk_test_zip" "$OHECO_ROOT/cache/downloads/$sdk_test_hashes[$sdk_test_component].zip"
  "$sdk_test_oo" install "ohos-sdk-$sdk_test_component"
done
sdk_test_headers="$OHECO_ROOT/packages/ohos-sdk-native/$sdk_test_version/sysroot/usr/include/linux"
# SHA-256 of the lowercase entries read directly from the original Release ZIP.
typeset -A sdk_test_header_hashes
sdk_test_header_hashes=(
  netfilter_ipv4/ipt_ecn.h ef151d3e9e8c299b87bcfaabe5db857b1557587d6a3ad25701285aa2eb17406b
  netfilter_ipv4/ipt_ttl.h d2d127e63e6f2c6e5b32f63680e27f0dd0b7f8841d67737dcf33be93c1ee9ac8
  netfilter/xt_rateest.h ffb391ab8db860ca5b3b6e710eba5b9f8e853f15ae1f2375a277bea1f5f28af5
  netfilter/xt_dscp.h 017b8f381d117d59b90901d11cfc483afcb6069c42b02a679551a2fa5b4a5347
  netfilter/xt_mark.h 1c9b6e2fd0cfd27205ec868bbc5fb1bf1840fcfdaa3346085b147dee2912214c
  netfilter/xt_tcpmss.h 0d128fee9819e2b5354321995a711c6855acf55d20b99ee27ab086ca0e8afcb0
  netfilter/xt_connmark.h 4f486650437a625e060d3c49ccc2a19ddafc473fb738459d5d3bb8fcb9f95024
  netfilter_ipv6/ip6t_hl.h eec6bea3a736c190486117182396b29a61cfdb086bd502602d12c438ea596e03
)
for sdk_test_rel in ${(k)sdk_test_header_hashes}; do
  sdk_test_actual=$(sha256sum "$sdk_test_headers/$sdk_test_rel")
  [[ ${sdk_test_actual%% *} = $sdk_test_header_hashes[$sdk_test_rel] ]]
  sdk_test_matches=0
  # Enumerate spelling: testing ! -e UPPER is invalid on a case-insensitive FS.
  for sdk_test_file in "$sdk_test_headers/${sdk_test_rel:h}"/*(N); do
    sdk_test_filename=${sdk_test_file:t}
    if [[ ${(L)sdk_test_filename} = ${sdk_test_rel:t} ]]; then
      [[ $sdk_test_filename = ${sdk_test_rel:t} ]]
      (( sdk_test_matches += 1 ))
    fi
  done
  [[ $sdk_test_matches = 1 ]]
done
print 'PASS all 8 lowercase header names and original ZIP content hashes'
export PATH="$OHECO_ROOT/bin:$PATH"
unset LD_LIBRARY_PATH
[[ $(command -v binary-sign-tool) = "$OHECO_ROOT/bin/binary-sign-tool" ]]
[[ ! -e "$OHECO_ROOT/bin/lldb" && ! -L "$OHECO_ROOT/bin/lldb" ]]
[[ ! -e "$OHECO_ROOT/bin/lldb-server" && ! -L "$OHECO_ROOT/bin/lldb-server" ]]
[[ -f "$OHECO_ROOT/packages/ohos-sdk-previewer/$sdk_test_version/oh-uni-package.json" ]]
"$OHECO_ROOT/bin/ld.lld@$sdk_test_version" --version
"$OHECO_ROOT/bin/lld-link@$sdk_test_version" --version
"$OHECO_ROOT/bin/clang@$sdk_test_version" --version
"$OHECO_ROOT/bin/cmake@$sdk_test_version" --version
"$OHECO_ROOT/bin/ninja@$sdk_test_version" --version
"$OHECO_ROOT/bin/hdc@$sdk_test_version" -v
"$OHECO_ROOT/bin/binary-sign-tool@$sdk_test_version" -h > "$sdk_test_dir/sign-help.txt"
# ETS/JS are installed as resources until their embedded tools are executable.
[[ ! -L "$OHECO_ROOT/bin/ets-es2abc" && ! -L "$OHECO_ROOT/bin/js-es2abc" ]]
cat > "$sdk_test_dir/hello.c" <<'SOURCE'
#include <stdio.h>
int main(void) { puts("PASS SDK compile, sign and run"); return 0; }
SOURCE
"$OHECO_ROOT/bin/clang@$sdk_test_version" --target=aarch64-linux-ohos --sysroot="$OHECO_ROOT/packages/ohos-sdk-native/$sdk_test_version/sysroot" "$sdk_test_dir/hello.c" -o "$sdk_test_dir/hello-unsigned"
"$OHECO_ROOT/bin/binary-sign-tool@$sdk_test_version" sign -inFile "$sdk_test_dir/hello-unsigned" -outFile "$sdk_test_dir/hello" -selfSign 1
chmod 755 "$sdk_test_dir/hello"
"$sdk_test_dir/hello"
cat > "$sdk_test_dir/check.cmake" <<'SOURCE'
if(NOT EXISTS "${CMAKE_ROOT}/Modules/CMake.cmake")
  message(FATAL_ERROR "CMake resources not found")
endif()
message(STATUS "PASS installed CMake resources: ${CMAKE_ROOT}")
SOURCE
"$OHECO_ROOT/bin/cmake@$sdk_test_version" -P "$sdk_test_dir/check.cmake"
"$sdk_test_oo" install ohos-sdk-toolchains
rm "$OHECO_ROOT/index/index.json"
for sdk_test_component in native toolchains ets js previewer; do
  "$sdk_test_oo" switch "ohos-sdk-$sdk_test_component" "$sdk_test_version"
done
"$OHECO_ROOT/bin/ld.lld" --version
"$sdk_test_oo" list
for sdk_test_component in native toolchains ets js previewer; do
  "$sdk_test_oo" remove "ohos-sdk-$sdk_test_component" --all
done
[[ ! -L "$OHECO_ROOT/bin/ld.lld@$sdk_test_version" ]]
[[ ! -L "$OHECO_ROOT/bin/binary-sign-tool@$sdk_test_version" ]]
print 'PASS native ZIP install with lowercase conflicts, launcher commands, compilation/signing, data package and offline lifecycle'
