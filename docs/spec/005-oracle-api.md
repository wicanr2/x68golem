# 005 — 觀測 API 與亂數控制

狀態：**DRAFT**（2026-09-05）——介面草案，尚未實作。

## 1. 為什麼要有這一層

remake 的 `go test` 要問原版問題，問法必須是「程式化、可重播、可斷言」。
送按鍵再等一段牆上時間然後猜畫面穩了沒，不是觀測，是碰運氣。

契約刻意與 `dosgolem` 的 `docs/spec/005` 對齊，位址型別換成 68000 的線性位址
（`006`：不需要 `IDAOffset` 換算）。

## 2. 生命週期

```go
o, err := x68.Load(x68.Config{
    Exe:    "SANMAIN.Z",          // 玩家自備
    Disks:  []string{diskA, diskB},
    IPLROM: iplrom, CGROM: cgrom, // 玩家自備
    Seed:   42,
})
o.RunUntil(sangokushi.CommandScreen)  // 條件式推進，不是 sleep
o.Keys("2\n5\n2\n3\n")                // 對齊指令數
snap := o.Save(); o.Restore(snap)     // 同一個狀態展開變體
```

`Save`／`Restore` 是完整的機器狀態複製。MAME 對 `x68000` 標
`savestate="unsupported"`，這是我們自己做一顆的實得好處之一。

## 3. 讀狀態

```go
n    := o.Word(0x77612)   // 戰場日
side := o.Long(0x77C3E)
o.OnCall(0x655B6, func(f *x68.Frame) {   // 觀測：讀參數，不改行為
    log("攻擊者=%d 目標=%d k=%d", f.Arg(0), f.Arg(1), f.Arg(2))
})
```

`Frame` 的參數取法屬於 **runtime 層**（XC／Lattice 的呼叫慣例），不是 CPU 層。

## 4. 亂數控制（這個專案存在的主要理由）

### 4.1 機器層只提供一個原語

```go
o.Intercept(addr, func(f *x68.Frame) (ret uint32, skip bool))
```

`skip == true` ⇒ **不執行原函式**，直接以 `ret` 返回。
機器層到此為止——它不知道什麼是亂數。

### 4.2 遊戲層把原語綁成亂數源

`sub_60580(n)` 回傳 `0..n−1`（`sangokushi_x68k_cht` 的 `docs/spec/02` §亂數，L2）。
`apps/sangokushi` 用上面的原語把它接成可控來源：

| 模式 | 行為 | 用途 |
|---|---|---|
| `Passthrough` | 不攔，**原版的 `FLOAT2.X` 自己算**（要先用 `Drivers` 把它載進來，`docs/findings/014`）| 基準線 |
| `Record` | 攔但照原值回，同時記下 `(pc, n, 值)` | 取得一條真實的亂數流 |
| `Replay` | 照錄下來的流逐項回放 | **同一條流、不同盤面**——正是這一輪定出一斉除數用的判準 |
| `Fixed(v)` | 每次都回同一個值 | 把亂數從方程式裡消掉，只剩規則 |
| `Seq(...)` | 依序回指定值，用完就報錯 | 精確構造情境（「這一次骰 0，下一次骰 499」）|

**報錯而不是回退到亂數**：用完就 fail，不要安靜地換一個來源——
安靜的替代品會讓「沒對到」看起來像「對到了」。

⚠ 直通模式**只有在 `FLOAT2.X` 真的載進來時才成立**。沒載的話
`$FE0E` 會落到我們的樁上，而那裡是 fail-closed 的——不會安靜地給一個
自己算的亂數。

### 4.3 這對對拍的意義

原版與 remake 不一樣時，只有兩個來源：規則不同，或亂數不同。
亂數變成輸入之後，第二個來源被消掉，剩下的差異**一定**是規則差異。

remake 那邊已經是可注入的 `Rand` 介面（`docs/spec/02` §亂數），
所以兩邊餵同一條 `Seq` 就能逐項比對，不必再談樣本數與信賴區間。

### 4.4 還沒定的

- 攔截點的觸發時機（進入時／返回時）與 `Frame` 的生命週期。
- `Record` 的流要不要含 `pc`：含了才分得出「同一個 `n` 被誰要走」，
  但也讓流變得對程式碼位址敏感。
- 亂數以外的非決定性來源（計時器、鍵盤時序）要不要一起納管。

## 5. 判準

`TestDeterminism`：同一顆種子、同一串輸入跑兩次，指令軌跡雜湊相同。
這一項不過，上面所有 API 都沒有意義。
