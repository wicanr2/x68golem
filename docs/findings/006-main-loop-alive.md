# 006 — 主迴圈活了：六千萬道指令、垂直同步 4,366 次

日期：2026-09-06

`SANMAIN.Z` 現在跑滿 `-steps 60000000` 而不是撞牆，垂直同步處理常式被叫了
4,366 次。它在畫開場。

```
== 服務呼叫（27 種）
  DOS call $3D _OPEN    x54     DOS call $3F _READ   x6
  FLOAT2   $0E rand()   x400
  IOCS     $6C _VDISPST x8733   IOCS $C6 _SP_REGST   x1955
  IOCS     $C4 _SP_DEFCG x75    IOCS $CF _SPALET     x10
== 主記憶體以外的存取
  text VRAM  寫 196,608
  CGROM      讀 400（272 個相異位址）
  DMAC       寫 6
```

`CGROM` 的讀取是文字：原版一般漢字直接從 CG ROM 取字模
（`sangokushi_x68k_cht` 的 `docs/re/05`）。我們現在回 0，所以字是空白的
——要看到字，得讓使用者自備 CGROM 掛進來（**不內嵌，那是 Sharp 的**）。

## 路上修掉的三個東西，都不是靠猜

### 1. 垂直同步處理常式以 `rte` 結尾，不是 `rts`

第一版假設 IOCS 用 `jsr` 呼叫它，於是只推 4 bytes 的回返位址。程式跑進
一片 0，而 **SSP 停在基準值 +2**——推 4、彈 6。那個 2 就是答案：
`rte` 彈一個 word 的 SR 加一個 long 的 PC。改推完整的例外堆疊框之後，
處理常式正常回來。

### 2. `_B_SUPER(0)` 已經在 supervisor 時要回 −1

程式自己在檢查：

```
0x071048  movea.l (d16,pc),a1
0x07104E  cmpa.l  #$FFFFFFFF,a1
0x071054  beq.w   跳過
0x07105A  trap    #15            ← 只有不是 −1 才還原
```

少了這一條，巢狀呼叫會回一個過期的 USP，程式把它當成「要還原的堆疊指標」
存起來。

### 3. `_B_SUPER` 要把系統的 supervisor 堆疊存起來、離開時放回去

`_B_SUPER(0)` 把 SSP 換成 USP（程式繼續用同一個堆疊，`rts` 才對得上）。
但如果離開時不把系統堆疊放回去，**下一次從 user mode 進來的例外會把
6 bytes 的框推到程式自己的堆疊底下**，蓋掉呼叫端還沒用到的返回位址。
症狀是 PC 變成 `0x2C`（例外向量表本身的位址），然後把向量表當程式碼跑。

這三個的共同形狀：**症狀離原因很遠，但每一個都有一個可以量的數字**
（SSP 的 +2、程式碼裡的 `cmpa.l #-1`、SSP 與 USP 的 4 bytes 漂移）。
`-log-services` 就是為了把那些數字擺到眼前——它會印出每一次服務呼叫
進入前與進入後的 SR、USP、SSP。

## 還沒有的

- CGROM：字模來源，使用者自備。
- 鍵盤：`_B_KEYINP`／`_B_KEYSNS` 還沒接，所以走不到「按鍵才前進」的畫面。
- 畫面輸出：text VRAM 有內容了，但還沒有把它畫成 PNG 的那一層（M3）。
