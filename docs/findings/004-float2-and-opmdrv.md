# 004 — 這個遊戲不是單獨跑的：`CONFIG.SYS` 載了兩個驅動

日期：2026-09-06

補上精靈與 BG 的 IOCS 之後，`SANMAIN.Z` 走到 **909,448 道指令**，
停在一個 **`$FE0D`** 的 F-line 指令：

```
0x06A180  4878 051C   pea    ($51C).l      ← 推一個 long 參數
0x06A184  4EB9 …      jsr    $6F28E
0x06F28E  202F 0004   move.l (4,sp),d0
0x06F292  FE0D                             ← 這裡
0x06F294  4E75        rts
```

`$FE0D` 不是 DOS call。Human68k 的 F-line 分兩段：

| 範圍 | 誰接 |
|---|---|
| `$FF00–$FF7F` | Human68k 本體的 DOS call |
| `$FE00–$FEFF` | **`FLOAT2.X`／`FLOAT4.X`** 的浮點與長整數運算 |

## 證據：Disk A 的 `CONFIG.SYS`

```
FILES   = 15
BUFFERS = 20
BELL    = \SYS\BEEP.SYS
DEVICE  = \SYS\OPMDRV.X
DEVICE  = \SYS\FLOAT2.X
SHELL   = COMMAND.X /B:30
VERIFY  = OFF
```

`AUTOEXEC.BAT` 只有一行 `SANMAIN`。

所以真機上的執行環境是 **Human68k ＋ OPMDRV.X ＋ FLOAT2.X**，
遊戲才啟動。`$FE0D` 在真機上有人接，不是非法指令。
同樣地，IOCS `$F0 _OPMDRV`（FM 音源）也是 `OPMDRV.X` 提供的。

## 這件事改變了範圍

x68golem 原本的假設是「載入 `.Z`，提供 DOS call 與 IOCS」。實際上還要
交代那兩個驅動。兩條路：

| | 自己用 Go 實作 | 把 `FLOAT2.X` 載進來跑 |
|---|---|---|
| 工作量 | 小（基本四則用 Go 的 `float64` 就是 IEEE 754）| 大（要實作 Human68k 的驅動載入與 `.X` 重定位）|
| 對拍風險 | **高** | 低 |

風險不對稱是重點。**浮點的差異不會當場爆，會變成某一場戰鬥的損失差 1**
——而這個專案存在的理由正是分辨「規則不同」與「別的東西不同」。
自己實作要能證明每一個運算與 `FLOAT2.X` 逐位元相同，那個證明的成本
不見得比直接跑它低。

暫定：**先把 `$FExx` 的號碼與參數記錄下來**（現在會 fail-closed 並印出
是哪一個號碼），累積出這個遊戲真正用到哪幾個運算；等清單收斂再決定。
只用到整數轉換與加減乘除的話，自己實作加上一份對照測試就夠；
用到超越函數（sin／exp／pow）就走載入 `FLOAT2.X` 那條。

⚠ **不要先寫一版「大概對」的浮點再說。** 那正是會產生「自洽但錯」結論的形狀。
