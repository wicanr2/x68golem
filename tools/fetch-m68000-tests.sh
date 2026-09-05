#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DEST="$ROOT/workplace/m68000-tests"
PIN=64b253116a3de04aaac4346c43680960dc9b67e5
URL=https://github.com/SingleStepTests/m68000.git

if [ -d "$DEST/.git" ]; then
  got=$(git -C "$DEST" rev-parse HEAD)
  [ "$got" = "$PIN" ] || {
    echo "m68000 corpus commit mismatch: got $got, want $PIN" >&2
    exit 1
  }
  echo "m68000 corpus already pinned at $PIN"
  exit 0
fi

mkdir -p "$ROOT/workplace"
git init -q "$DEST"
git -C "$DEST" remote add origin "$URL"
git -C "$DEST" fetch --depth 1 origin "$PIN"
git -C "$DEST" checkout -q --detach FETCH_HEAD
got=$(git -C "$DEST" rev-parse HEAD)
[ "$got" = "$PIN" ] || {
  echo "m68000 corpus commit mismatch after fetch: got $got, want $PIN" >&2
  exit 1
}
echo "m68000 corpus ready at $PIN"
