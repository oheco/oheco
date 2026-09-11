#!/usr/bin/zsh
# Run with the native fixture server already listening on loopback:18808.
set -eu
setopt PIPE_FAIL
test_root=$(mktemp -d "$HOME/.oheco-e2e.XXXXXXXX")
trap 'rm -rf -- "$test_root"' EXIT
export OHECO_ROOT="$test_root/root with spaces"
export OHECO_INDEX_URL=http://127.0.0.1:18808/index/v2/index.json
export ZDOTDIR="$test_root/zsh"
mkdir -p "$ZDOTDIR"
print -r -- '# existing user configuration' > "$ZDOTDIR/.zshrc"
curl -fsSL http://127.0.0.1:18808/install.sh | zsh
oo_bin="$OHECO_ROOT/bin/oo"
"$oo_bin" --version
"$oo_bin" search
"$oo_bin" info oheco
"$oo_bin" install oheco@0.1.0 --no-switch
# Exercise upgrading from the previous release using its v1 endpoint.
"$oo_bin" switch oheco 0.1.0
"$OHECO_ROOT/bin/oo@0.1.0" remove oheco@0.3.1
OHECO_INDEX_URL=http://127.0.0.1:18808/index/v1/index.json "$OHECO_ROOT/bin/oo@0.1.0" update
"$OHECO_ROOT/bin/oo@0.1.0" install oheco
[[ $("$oo_bin" --version) = 'oo 0.3.1 '* ]]
"$oo_bin" update
"$oo_bin" list
# The second bootstrap must not duplicate or erase zsh configuration.
curl -fsSL http://127.0.0.1:18808/install.sh | zsh
rc_content=$(<"$ZDOTDIR/.zshrc")
[[ $rc_content = *'# existing user configuration'* ]]
[[ ${#${(M)${(f)rc_content}:#'# oheco: command path'}} = 1 ]]
zsh -f -c 'source "$ZDOTDIR/.zshrc"; command -v oo; oo --version'
# A normal new interactive zsh must load .zshrc without an explicit source.
zsh -i -c '[[ $(command -v oo) = "$OHECO_ROOT/bin/oo" ]] && oo --version'
# Switching and removing must work even after the index is gone.
rm "$OHECO_ROOT/index/index.json"
"$oo_bin" switch oheco 0.1.0
"$OHECO_ROOT/bin/oo@0.3.1" switch oheco 0.3.1
"$oo_bin" remove oheco@0.1.0
[[ ! -L "$OHECO_ROOT/bin/oo@0.1.0" ]]
"$oo_bin" list
print 'PASS native bootstrap, PATH, v1 client upgrade, idempotence and offline lifecycle'
