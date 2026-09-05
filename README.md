# x68golem — X68000 魔像

**無頭、決定性、可以當 Go 套件 import 的 X68000 執行器**，為程式化觀測而寫。

第一個案例是 KOEI 1988 X68000 版《三國志》（`SANMAIN.Z`）與
[`sangokushi_x68k_cht`](https://github.com/wicanr2/sangokushi_x68k_cht) 的對拍，
但目標不是只跑那一支。姊妹專案 [`dosgolem`](https://github.com/wicanr2/dosgolem)
在 x86 ＋ DOS 上做同一件事，上面兩層的契約刻意對齊。

## 為什麼不用 MAME

MAME 是對的參考實作，**這個專案不取代它**——畫面驗收拿 MAME 當 oracle。
但有三件事它給不了：

| | MAME 0.264（實際量過）| x68golem |
|---|---|---|
| 攔截函式呼叫讀參數 | Lua 的 `debug:bpset` 是 function，呼叫下去**整個行程當場消失** | 語言層 callback |
| 快照分支 | `x68000` driver 標 `savestate="unsupported"` | 記憶體結構直接複製 |
| **控制亂數** | **給不了**——外掛只能觀測，不能決定 | `docs/spec/005` §4 |

第三項是主要理由。對拍時「原版跟 remake 為什麼不一樣」只有兩個來源：
規則不同，或亂數不同。**亂數不受控的時候這兩個分不開**，只能多跑幾場再談
信賴區間；把亂數變成輸入，對拍就從統計推論變成逐項比對。

## 現況

**M1 CPU 完成**：MC68000 核心通過 SingleStepTests/m68000 v1 的 **240,168 筆**語料
（`tools/go.sh test ./m68k/`）。核心是從
[`atari-talos-ai-toolkit`](https://github.com/wicanr2/atari-talos-ai-toolkit)
原封複製的——Atari ST 與 X68000 都是 MC68000，這一層沒有平台差異
（`m68k/PROVENANCE.md`）。已知缺口：`addx.l`（`SANMAIN.Z` 用到 1 次）。

**M2 完成：《三國志》開到標題畫面**（`docs/findings/008`）

```
1:NEW GAME  2:LOAD DATA (1-2)? ▌
```

再按一次會走到劇本選單（董卓打倒 189年 … 三国の時代 215年）。
字是原版自己從 CGROM 取的字模畫的。走過 3,884 個相異位址，
`TestBootToTitle` 把這條路釘住（需要玩家自備的執行檔、磁碟與 CGROM，
缺一樣就 skip）：`.Z` 載入器、Human68k
交棒契約、DOS call／IOCS 服務攔截、FAT12 檔案系統、軟碟、精靈與 BG、
CRTC 的動作位元都通了。遊戲自己把 `KANJIF.DAT`、`KAODATA` 與四個 `.PAK`
從磁碟開出來，大小全部正確。

**亂數控制已經可用**（`docs/findings/005`）——這是這個專案的主要理由。
《三國志》的亂數不在遊戲裡：`sub_60580(n) == rand() % n`，而 `rand()` 是
`CONFIG.SYS` 載的 `FLOAT2.X` 透過 F-line `$FE0E` 提供的。掛在那裡是
**機器層的服務邊界**，換一個 X68000 程式一樣成立。
`cmd/probe -rand-fixed 12345` 可以讓整個開機流程跑在受控的亂數上。

**尚未開始**：畫面輸出、鍵盤、垂直同步中斷、觀測 API。
**現在還不是能跑遊戲的模擬器**，沒有的能力一律當場失敗，不會假裝成功。

| 里程碑 | 目標 | 驗收 | 狀態 |
|---|---|---|---|
| M0 | 服務面普查（`cmd/probe`）| 跑一次列出「用到而未實作」的 DOS call／IOCS／硬體位址 | **完成**（`docs/findings/001`）|
| M1 | 68000 核心 | SingleStepTests 全綠 | **完成**（240,168 筆）|
| M2 | 載入器 ＋ 11 個 DOS call ＋ 前 10 個 IOCS | 跑到標題畫面 | **完成**（9 個 DOS call ＋ 30 個 IOCS ＋ FAT12 ＋ 軟碟 ＋ 精靈 ＋ DMAC ＋ 鍵盤；`TestBootToTitle`）|
| M3 | text／graphics 平面 ＋ 調色盤 | 標題畫面與 MAME 的索引截圖逐點相同 | **進行中**（text 平面已經畫得出來、`-shot` 可輸出；還沒對 MAME 逐點比）|
| M4 | 鍵盤 ＋ 計時器 | 冷啟動走到指令畫面、下完一個月的指令 | 未開始 |
| M5 | 觀測 API（快照、`OnCall`、**亂數控制**）| 用 `go test` 重跑一斉攻撃的除數對照 | 未開始 |
| M6 | 分層：`runtime/xc` 與 `apps/` 拆開 | 第二個 X68000 程式不必 fork | 未開始 |

## 用法

```
tools/fetch-m68000-tests.sh    # 抓 68000 語料（182 MB，不進版控）
tools/go.sh test ./...         # 建置與測試都走 docker
tools/sync-m68k.sh --check     # m68k/ 有沒有跟上游分岔

# 開機到標題畫面（執行檔、磁碟、CGROM 都是玩家自備）
tools/go.sh run ./cmd/probe -z SANMAIN.Z -disks A.dim,B.dim \
    -cgrom cgrom.dat -latch-io -rand-fixed 12345 \
    -steps 200000000 -keys " \n" -shot title.png
```

## 不散布原版素材

本儲存庫不含 `SANMAIN.Z`、磁碟映像、IPL ROM 或 CGROM。
需要原版的測試**缺檔就 skip**——安靜的替代品會讓「還沒做完」看起來像做完了。

## 授權

RRSAL-1.0，見 [`LICENSE`](LICENSE)。
