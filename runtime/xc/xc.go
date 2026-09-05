// Package xc 是第三層：XC／Lattice C 在 X68000 上的呼叫慣例。
//
// 它知道「一個 C 函式的參數長什麼樣」，但**不知道任何一支特定函式**——
// 那是第四層（`apps/`）的事。分層見 `docs/spec/006`。
//
// 慣例（從 SANMAIN.Z 的呼叫點量出來的，L2）：
//
//   - 參數由右往左推到堆疊上，所以第一個參數離返回位址最近。
//   - 函式進入的當下，`(sp)` 是返回位址，第一個參數在 `4(sp)`。
//   - 回傳值放 D0（long）。
//   - 呼叫端自己收拾堆疊（`addq.l #n,sp` 緊接在呼叫之後）。
//
// 證據：`$FF4A _SETBLOCK` 前面是 `move.l d1,-(sp)` / `move.l a5,-(sp)`，
// 後面是 `addq.l #8,sp`；`sub_60580` 進來第一件事是 `move.l (8,sp),d2`
// ——它先推了 d2，所以參數從 `4(sp)` 變成 `8(sp)`
// （`x68golem` 的 `docs/findings/001`、`005`）。
package xc

import "github.com/wicanr2/x68golem/internal/x68k"

// Long 讀第 n 個 long 參數（從 0 起算），假設函式還沒動過堆疊。
func Long(f *x68k.Frame, n int) (uint32, error) { return f.ArgLong(n) }

// Word 讀堆疊上偏移 off 處的 word（off 從第一個參數起算）。
func Word(f *x68k.Frame, off uint32) (uint16, error) { return f.ArgWord(off) }

// CString 讀一個 null 結尾字串，最多 max bytes。
//
// **讀不到結尾就回錯誤，不要截斷後假裝成功**：截斷過的字串看起來很正常，
// 而錯誤會在很久以後以「檔案開不起來」的形式出現。
func CString(f *x68k.Frame, addr uint32, max int) (string, error) {
	return f.Machine().ReadCString(addr, max)
}

// Call 依 XC 的呼叫慣例呼叫一支函式，回傳 D0。
//
// 參數一律當 long 推。**word 參數也推 long**——C 的預設引數提升本來就這樣，
// 而 SANMAIN.Z 的呼叫點量到的也是這樣（`pea ($64).w` 推的是 long）。
func Call(m *x68k.Machine, addr uint32, args ...uint32) (uint32, error) {
	return m.CallSubroutine(addr, args...)
}
