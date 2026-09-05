package x68k

import "fmt"

// 函式攔截：在任意位址上觀測或取代一支函式。
//
// 這是 `docs/spec/005` §3／§4 的原語。`OnCall` 只看不改；`Intercept` 可以
// **不執行原函式、直接以指定的值返回**。亂數控制、把某支函式換成固定行為、
// 記錄參數，都由這兩個組出來。
//
// 攔截點是 PC，檢查在每一步執行**之前**，所以拿到的是函式剛進入時的狀態：
// 參數還在堆疊上、返回位址在 `(sp)`。

// Frame 是攔截點看到的呼叫框。
type Frame struct{ m *Machine }

// ArgLong 讀第 n 個 long 參數（C 的堆疊慣例，返回位址在 SP+0）。
func (f *Frame) ArgLong(n int) (uint32, error) { return f.m.ArgLongAt(uint32(4 + n*4)) }

// ArgWord 讀堆疊上偏移 off 處的 word（off 從返回位址之後算起）。
func (f *Frame) ArgWord(off uint32) (uint16, error) { return f.m.ArgWord(4 + off) }

// Machine 回傳底下的機器，讓攔截點讀暫存器與記憶體。
func (f *Frame) Machine() *Machine { return f.m }

// InstallHook 在 addr 上裝一個只看不改的攔截點。
func (m *Machine) InstallHook(addr uint32, fn func(*Frame)) {
	if m.hooks == nil {
		m.hooks = map[uint32]func(*Frame){}
	}
	m.hooks[addr] = fn
}

// InstallIntercept 在 addr 上裝一個可以取代原函式的攔截點。
//
// fn 回傳 (回傳值, 是否略過原函式)。略過時我們替它做 `rts`：
// 從堆疊彈出返回位址並跳回去，回傳值放 D0。
func (m *Machine) InstallIntercept(addr uint32, fn func(*Frame) (uint32, bool)) {
	if m.intercepts == nil {
		m.intercepts = map[uint32]func(*Frame) (uint32, bool){}
	}
	m.intercepts[addr] = fn
}

// serviceHooks 在執行 pc 上的指令之前處理攔截點。
// 回傳 true 表示這一步已經處理掉了（原指令沒有執行）。
func (m *Machine) serviceHooks(pc uint32) (bool, error) {
	if fn, ok := m.hooks[pc]; ok {
		fn(&Frame{m: m})
	}
	fn, ok := m.intercepts[pc]
	if !ok {
		return false, nil
	}
	ret, skip := fn(&Frame{m: m})
	if !skip {
		return false, nil
	}
	// 替它做 rts。
	sp := m.SP()
	target, err := m.readLong(sp)
	if err != nil {
		return false, err
	}
	if target&1 != 0 {
		return false, fmt.Errorf("x68k: 攔截 0x%06X 之後的返回位址 0x%X 是奇數", pc, target)
	}
	m.setSP(sp + 4)
	m.SetResult(ret)
	return true, m.resume(target)
}

// setSP 設定目前有效的堆疊指標。
func (m *Machine) setSP(v uint32) {
	if m.CPU.State.SR&0x2000 != 0 {
		m.CPU.State.SSP = v
		return
	}
	m.CPU.State.USP = v
}
