# `m68k/` 的來源

這個套件是從 [`atari-talos-ai-toolkit`](https://github.com/wicanr2/atari-talos-ai-toolkit)
的 `internal/m68k` **原封不動複製**過來的（同一位著作權人）。

| | |
|---|---|
| 上游 commit | `313996f31aa8c61473cfb0f89b0398c07fd35b98` |
| 複製日期 | 2026-09-05 |
| 上游路徑 | `internal/m68k/` |

Atari ST 與 X68000 都是 MC68000，CPU 這一層沒有平台差異——**所以這裡不該有
第二份實作**（`sangokushi_x68k_cht/CLAUDE.md` §7 第 6 條：一條規則只留一份實作）。

## 規則

**不要在這裡改 CPU 的行為。** 發現錯誤就回上游改，改完重新同步：

```
tools/sync-m68k.sh --check    # 只比對，有差異就非零離開
tools/sync-m68k.sh            # 從上游重新複製並更新本檔的 commit
```

它是 `internal/` 套件，Go 不允許跨模組 import，所以只能複製。
**複製的代價是會分岔**——`--check` 存在就是為了讓分岔被看見，而不是等到
某一天兩邊的除以零行為不一樣才發現。CI 與 `tools/go.sh test` 都會跑它。

真的需要長期共用時，正解是把它從兩邊都抽成獨立 module；
在那之前，這份複製件的唯一真相是上游。

## 已知的 `go vet` 抱怨（不要在這裡修）

```
m68k/cpu.go:54: method ReadByte(address uint32, functionCode uint8) (byte, error)
                should have signature ReadByte() (byte, error)
```

`go vet` 把 `ReadByte`／`WriteByte` 當成 `io.ByteReader`／`io.ByteWriter` 的
實作在檢查，但這裡的 `Bus` 是匯流排介面，同名只是巧合。
**這是上游的介面設計，不在複製件裡改**——要改回上游改。
