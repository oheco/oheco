#!/usr/bin/zsh
set -eu
probe=$(mktemp -d "$HOME/.oheco-probe.XXXXXXXX")
trap 'rm -rf -- "$probe"' EXIT
mkdir -p "$probe/bin" "$probe/packages/probe/1/bin"
cp /storage/Users/currentUser/dev/ohos/go/bin/gofmt "$probe/packages/probe/1/bin/gofmt"
chmod 755 "$probe/packages/probe/1/bin/gofmt"
ln -s ../packages/probe/1/bin/gofmt "$probe/bin/gofmt@1"
ln -s gofmt@1 "$probe/bin/gofmt"
"$probe/bin/gofmt" -h >/dev/null 2>&1
ln -s gofmt@1 "$probe/bin/.next"
mv -f "$probe/bin/.next" "$probe/bin/gofmt"
"$probe/bin/gofmt" -h >/dev/null 2>&1
print 'PASS host home: executable copy, chmod, two-level symlinks, rename'
