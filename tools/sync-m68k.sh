#!/usr/bin/env bash
# `m68k/` 是從 atari-talos-ai-toolkit 的 internal/m68k 複製來的（見 m68k/PROVENANCE.md）。
# Atari ST 與 X68000 都是 MC68000，這一層沒有平台差異，不該有第二份實作。
#
#   tools/sync-m68k.sh --check   # 只比對；有差異就非零離開（CI 用）
#   tools/sync-m68k.sh           # 從上游重新複製，並更新 PROVENANCE 的 commit
#
# 上游位置可用 X68GOLEM_M68K_UPSTREAM 覆寫。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UP="${X68GOLEM_M68K_UPSTREAM:-$HOME/cht/atari-talos-ai-toolkit}"
SRC="$UP/internal/m68k"

[ -d "$SRC" ] || { echo "找不到上游 $SRC（用 X68GOLEM_M68K_UPSTREAM 指路）" >&2; exit 3; }

check() {
    local diff_out
    diff_out="$(diff -ru "$SRC" "$ROOT/m68k" -x PROVENANCE.md || true)"
    if [ -n "$diff_out" ]; then
        echo "== m68k/ 與上游有差異"
        echo "$diff_out"
        echo
        echo "CPU 的修正要回上游改（$UP），不要改這份複製件；改完跑 tools/sync-m68k.sh。"
        return 1
    fi
    echo "m68k/ 與上游一致（$(git -C "$UP" rev-parse --short HEAD)）"
}

if [ "${1:-}" = "--check" ]; then check; exit; fi

cp "$SRC"/*.go "$ROOT/m68k/"
new="$(git -C "$UP" rev-parse HEAD)"
sed -i "s/^| 上游 commit | \`.*\` |/| 上游 commit | \`$new\` |/" "$ROOT/m68k/PROVENANCE.md"
sed -i "s/^| 複製日期 | .* |/| 複製日期 | $(date +%Y-%m-%d) |/" "$ROOT/m68k/PROVENANCE.md"
echo "已同步到 $new"
