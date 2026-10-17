#!/bin/sh
set -eu
RELAY_PROJECT_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$RELAY_PROJECT_ROOT"
mkdir -p build
# Archive the committed tree only: ignored/untracked credentials never enter it.
git diff --quiet && git diff --cached --quiet || {
 echo 'Commit tracked changes before building a source release.' >&2
 exit 1
}
RELAY_VERSION=$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' options.go)
test -n "$RELAY_VERSION"
RELAY_ARCHIVE="build/f50_pro_relay-v${RELAY_VERSION}-source.tar.gz"
RELAY_TAR=$(mktemp "${TMPDIR:-/tmp}/f50-relay-source.XXXXXX")
trap 'rm -f "$RELAY_TAR" "$RELAY_ARCHIVE.tmp"' EXIT HUP INT TERM
git archive --format=tar --output="$RELAY_TAR" HEAD
gzip -n -c "$RELAY_TAR" > "$RELAY_ARCHIVE.tmp"
mv "$RELAY_ARCHIVE.tmp" "$RELAY_ARCHIVE"
LC_ALL=C shasum -a 256 "$RELAY_ARCHIVE" > "$RELAY_ARCHIVE.sha256"
echo "Built standalone module: $RELAY_ARCHIVE"
