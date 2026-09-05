# x68golem — 給 Claude 的專案規則

## 這是什麼

無頭、決定性、可以當 Go 套件 import 的 **X68000** 執行器（`README.md`）。
第一個案例是《三國志》對拍，但**目標不是只跑那一支**——分層與判準見
`docs/spec/006`。

## 動手前

1. 讀 `docs/spec/001-scope-and-mvp.md`。
2. **SDD：spec 齊了才實作。只有標 `READY` 的規格可以動手。**
3. 通用規則（硬規則、docker 邊界）在 `sangokushi_x68k_cht/CLAUDE.md`，這裡不重抄。

## `[HARD]` 硬規則

- **不得散布原版素材。** 不含 `SANMAIN.Z`、磁碟、IPL ROM、CGROM。
  需要原版的測試**缺檔就 skip**，不用自製代用品。
- **CGROM 不內嵌**：那是 Sharp 的。對拍時由原版自己去取，使用者自備。
- **建置與測試一律走 docker**（`tools/go.sh`）。只清理自己建立的 container；
  禁止任何 `docker image/system/volume/builder prune` 或 `rmi`。
- **git 身分一律 `wicanr2@gmail.com`。** 進 repo 先看 `git config user.email`，
  再跑一次 `git log --format=%ae | sort -u` 看歷史。
- **`m68k/` 是上游的原封複製件，一個字都不要改**（`m68k/PROVENANCE.md`）。
  CPU 的修正回 `atari-talos-ai-toolkit` 改，改完 `tools/sync-m68k.sh`。
  要加測試就加在 `coverage/`，不要加在 `m68k/`。
- **CPU 的驗收判準是「全部通過」，不是「大部分通過」**（`docs/spec/002` §2）。
- **沒實作的能力一律 fail-closed**，不可回 0 假裝成功。
- **測試語料不進版控**（182 MB）。用 `tools/fetch-m68000-tests.sh`。

## 從語料反推規則時

SingleStepTests 是硬體產生的，它與手冊衝突時**以它為準**。
反推出來的規則要寫進 `docs/spec/`，講清楚是從語料反推的、
手冊怎麼寫是錯的，並在程式碼註解標出哪一個檔、幾筆資料支持。
