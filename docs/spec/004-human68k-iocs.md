# 004 — Human68k DOS call 與 IOCS

狀態：**DRAFT**（2026-09-05）

## 範圍由普查決定，不由手冊決定

`SANMAIN.Z` 實際用到的（`sangokushi_x68k_cht` 的 `docs/design/06` §4.1(b)）：

| | 站點 | 種類 |
|---|---|---|
| DOS call（F-line `$FF00–$FF7F`）| 28 | **11**：`_INPOUT`／`_GETSS`／`_SUPER`／`_CONCTRL`／`_CREATE`／`_OPEN`／`_CLOSE`／`_READ`／`_WRITE`／`_SEEK`／`_IOCTRL` |
| IOCS（`trap #15`）| 107 | **33** |

合計 44 個進入點。DOS call 全部落在 `0x6E000` 以上的執行期包裝層。

## fail-closed

**沒實作的呼叫號要當場失敗並印出是誰呼叫的**，不可回 0 假裝成功。
安靜的成功會讓上層在幾百萬個指令之後畫錯一格，而回頭找不到原因。

`cmd/probe` 把這件事變成一次跑完就有的清單：跑一輪，列出
「用到而未實作」的 DOS call／IOCS／硬體位址。這是 M0 的全部內容，
也是 §003 那個「普查是下界」風險的解法。

## 未定

- `_SUPER` 的特權切換與我們的 SR 模型怎麼對。
- 檔案 I/O 直接接到宿主的磁碟映像讀取，還是走 FDC？
  先走 DOS call 層（FDC 的 7 個站點多半在錯誤處理）。
