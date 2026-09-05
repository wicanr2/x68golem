# 006 — 分層

狀態：**READY**（2026-09-05）

四層，照 `dosgolem` 的骨架，換掉最底下兩層：

| 層 | 套件 | 內容 |
|---|---|---|
| **機器** | `internal/m68k`、`internal/human68k`、`internal/x68k` | 68000 核心、DOS call ＋ IOCS、text/graphics 平面、CRTC／調色盤、MFP 計時器、鍵盤 |
| **觀測** | `oracle/` | `Load`／`RunUntil`／`Keys`／`Save`／`Restore`／`Word`／`OnCall`／`Intercept`／`RNG` |
| **runtime** | `runtime/xc/` | XC／Lattice C 的慣例：呼叫框、`printf` 家族、`rand` 的 LCG |
| **程式** | `apps/<遊戲>/` | 位址常數、狀態讀取、流程函式、攔截點 |

## 判準：第二支程式不必 fork

第四層以外的任何一層出現「三國志」這三個字，就是分層破了。
位址常數、`sub_60580` 是哪一支、戰場狀態在哪裡——**全部屬於 `apps/sangokushi`**。

## 位址型別比 DOS 那邊簡單

68000 是平坦定址，`.Z` 是固定位址的平坦映像
（`sangokushi_x68k_cht` 的 `docs/re/01`），**IDA 的線性位址就是執行期位址**。
`dosgolem` 那條「換載入位置就要重算而且不會報錯」的坑在這裡不存在，
所以本專案不設 `IDAOffset` 這種型別——沒有需要換算的東西，就不要留換算的介面。
