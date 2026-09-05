#!/usr/bin/env bash
# Go 走 docker，不裝到系統環境。
#
#   tools/go.sh build ./...
#   tools/go.sh test ./internal/m68k -run TestSingleStep -v
#
# 要跑原版執行檔時，用 X68GOLEM_ORIG 指到玩家自己的素材目錄，
# 它會**唯讀**掛到容器裡的 /orig：
#
#   X68GOLEM_ORIG=~/cht/sangokushi/workplace tools/go.sh run ./cmd/probe \
#       -z /orig/orig/x68k/SANMAIN.Z
#
# 本專案不含任何原版檔案，掛載一律唯讀（`ro`），容器也不會把它們帶出去。
#
# module cache 放 workplace/（gitignore），避免每次重抓。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${X68GOLEM_GO_IMAGE:-golang:1.24-bookworm}"
RUN_UID="${X68GOLEM_RUN_UID:-$(id -u)}"
RUN_GID="${X68GOLEM_RUN_GID:-$(id -g)}"

mkdir -p "$ROOT/workplace/gocache" "$ROOT/workplace/gomodcache"

# docker 預設不繼承 shell 的環境；交叉編譯要靠這幾個。
PASS=()
for v in GOOS GOARCH CGO_ENABLED X68GOLEM_TEST_Z X68GOLEM_TEST_DISK X68GOLEM_TEST_DISKS X68GOLEM_TEST_CGROM X68GOLEM_TEST_MAME_TVRAM X68GOLEM_TEST_MAME_GVRAM X68GOLEM_TEST_X; do
  [[ -n "${!v:-}" ]] && PASS+=(-e "$v=${!v}")
done

MOUNTS=()

# 68000 語料（SingleStepTests/m68000，182 MB，不進版控）。
# 預設抓 workplace/m68000-tests，可用 X68GOLEM_M68000_TESTS 指到別處
# （例如 atari-talos-ai-toolkit 已經抓好的那一份，省一次下載）。
#
# ⚠ 容器裡的環境變數叫 **TALOS_M68000_TESTS**，不是 X68GOLEM_ 開頭的——
# 因為 `m68k/` 是上游的原封複製件，**不改一個字**（`m68k/PROVENANCE.md`）。
# 換掉這個名字就等於改了複製件，下一次 sync 會衝突。
CORPUS="${X68GOLEM_M68000_TESTS:-$ROOT/workplace/m68000-tests}"
CORPUS="$(readlink -f "$CORPUS" 2>/dev/null || echo "$CORPUS")"
if [[ -d "$CORPUS/v1" ]]; then
  MOUNTS+=(-v "$CORPUS:/corpus:ro")
  PASS+=(-e "TALOS_M68000_TESTS=/corpus/v1")
fi

# 原版素材唯讀掛載。沒設就不掛——容器預設看不到任何原版檔案。
if [[ -n "${X68GOLEM_ORIG:-}" ]]; then
  MOUNTS+=(-v "$(cd "$X68GOLEM_ORIG" && pwd):/orig:ro")
fi

exec timeout "${X68GOLEM_TIMEOUT:-30m}" docker run --rm --network none \
  --memory "${X68GOLEM_MEM:-4g}" --cpus "${X68GOLEM_CPUS:-4}" --pids-limit 256 \
  --log-opt max-size=10m --log-opt max-file=3 \
  -u "$RUN_UID:$RUN_GID" \
  -v "$ROOT:/src" \
  -v "$ROOT/workplace/gocache:/gocache" \
  -v "$ROOT/workplace/gomodcache:/gomodcache" \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -e HOME=/tmp -e GOFLAGS=-mod=mod \
  "${PASS[@]}" "${MOUNTS[@]}" -w /src "$IMAGE" go "$@"
