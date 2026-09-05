# 003 — 開機走到精靈初始化：42 萬道指令

日期：2026-09-06

一路把擋路的服務補上去，`SANMAIN.Z` 現在走到 **424,219 道指令**，
停在 IOCS `$C0 _SP_INIT`（精靈初始化）。

## 走了多遠，是被什麼擋下來的

| 補上的東西 | 走到 |
|---|---|
| （只有清 0 的暫存器）| 82 |
| Human68k 交棒契約 | 106 |
| `_SETBLOCK`、`_INTVCS` | 2,345 |
| `_IOCTRL`、`_FNCKEY` | 3,520 |
| `_CONCTRL` 模式 0–3／10／11／14–18 | 3,523 |
| CRTC 動作位元、`_B_SUPER`、`_TGUSEMD`、`_CRTMOD`、`_G_CLR_ON`、`_TVCTRL` | 153,616 |
| `_B_DRVCHK`（有磁片）| 286,787 |
| FAT12 ＋ `_OPEN`／`_READ`／`_SEEK`／`_CLOSE` | **424,219** |

## 檔案系統對上了

第一次讓遊戲自己去開檔，開出來的東西與磁碟目錄一致：

```
a:KANJIF.DAT（28672 bytes）
a:KAODATA（462192 bytes）
a:NEWOPEN.PAK（130601 bytes）
a:LRYUU.PAK（26134 bytes）
a:RRYUU.PAK（26090 bytes）
a:ENDING.PAK（202644 bytes）
```

每一個大小都與 `sangokushi_x68k_cht` 記的一致（`KAODATA` 462 KB、
`ENDING.PAK` 202,644）。**這不是「看起來有讀到東西」**：檔案長度是從
目錄項目讀的，內容是照 FAT12 的叢集鏈接出來的，長度對得上表示兩件事都對。

順帶證實了一件舊斷言：**Disk A 的 BPB 是非標準值**
（`sangokushi_x68k_cht` 的 `CLAUDE.md` §2.1）。`NewFAT` 的退回路徑
在真磁碟上會觸發，`TestFATRealDisk` 印出「退回預設參數：true」。

## 名稱的來源分兩種，不要混

- **從行為推的**（L2）：`_SETBLOCK`／`_INTVCS` 的參數形狀、`_B_SUPER`
  的用法、CRTC 動作位元兩個方向都被等。證據是那幾道指令，寫在對應的
  函式檔頭。
- **平台公開規格**：IOCS 與 DOS call 的號碼對名稱、`_CONCTRL` 的模式表、
  `_B_DRVCHK` 的狀態位元，取自 Data Crystal 的 X68000 手冊整理。
  照 `retro-hardware-spec-first` 的原則，平台規格夠用時就不要把 BIOS
  當遊戲來反組譯。

兩者都不是「猜」。**沒有證據的地方一律 fail-closed**：`_CONCTRL` 碰到
沒實作的模式會回錯誤而不是回 0，因為模式的參數長度各不相同，猜錯會把
堆疊讀歪——而那種錯不會當場爆，會在很久以後變成一個畫錯的字。

## 還沒做

- 寫入：`_CREATE`／`_WRITE`。存檔會走到那裡，做的時候要一起想
  「寫回磁碟映像」該不該發生（對拍時多半不該）。
- 精靈（`$C0 _SP_INIT` 以下）、調色盤、鍵盤、垂直同步中斷（`$6C _VDISPST`）。
- 「目前磁碟機」的概念：現在是從 0 號機開始找第一個同名檔案。
