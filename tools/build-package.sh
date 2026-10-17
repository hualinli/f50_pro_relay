#!/bin/sh
set -eu
RELAY_PROJECT_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$RELAY_PROJECT_ROOT"
mkdir -p build
# Only the independent module is distributed; no panel, binaries or private device data.
COPYFILE_DISABLE=1 LC_ALL=C tar -czf build/f50_pro_relay-v0.5.0-source.tar.gz --exclude='./build' --exclude='./.git' -C . .
LC_ALL=C shasum -a 256 build/f50_pro_relay-v0.5.0-source.tar.gz > build/f50_pro_relay-v0.5.0-source.tar.gz.sha256
echo 'Built standalone module: build/f50_pro_relay-v0.5.0-source.tar.gz'
