package x68k

import "fmt"

// 鍵盤：IOCS `$00 _B_KEYINP`（等到有鍵為止）與 `$01 _B_KEYSNS`（不等）。
//
// 回傳值的低 byte 是字碼，高 byte 是掃描碼。目前只送字碼——
// 需要掃描碼的地方（方向鍵、功能鍵）等有實際用到再補，**不先編一份對照表**。
type Keyboard struct {
	queue []uint32
}

// Push 把一個鍵排進佇列。
func (k *Keyboard) Push(v uint32) { k.queue = append(k.queue, v) }

// PushString 把一串 ASCII 排進佇列。
func (k *Keyboard) PushString(s string) {
	for _, r := range []byte(s) {
		k.Push(uint32(r))
	}
}

// Peek 看一眼但不取走；空的回 0。
func (k *Keyboard) Peek() uint32 {
	if len(k.queue) == 0 {
		return 0
	}
	return k.queue[0]
}

// Pop 取走一個；空的回 0。
func (k *Keyboard) Pop() uint16 {
	if len(k.queue) == 0 {
		return 0
	}
	v := k.queue[0]
	k.queue = k.queue[1:]
	return uint16(v)
}

// Len 是還剩幾個鍵沒被取走。
func (k *Keyboard) Len() int { return len(k.queue) }

// InstallKeyboard 登記鍵盤相關的 IOCS。
func (m *Machine) InstallKeyboard() {
	if m.Keys == nil {
		m.Keys = &Keyboard{}
	}
	m.IOCSCalls[0x00] = iocsKeyinp
	m.IOCSCalls[0x01] = iocsKeysns
}

// iocsKeysns 是 `$01 _B_KEYSNS`：看有沒有鍵，沒有就回 0。
// **沒有人按鍵的時候回 0 是真話**，不是樁：原版也是這樣。
func iocsKeysns(m *Machine) error {
	m.SetResult(m.Keys.Peek())
	return nil
}

// iocsKeyinp 是 `$00 _B_KEYINP`：等到有鍵為止。
//
// 真機上這裡會停下來等人；我們沒有「等」這件事，所以**佇列空了就當場失敗**，
// 而不是回 0 假裝有人按了 NUL。回 0 會讓遊戲以為收到一個不存在的按鍵，
// 走進一條沒有人選過的路——那種錯不會當場爆，會變成一個對不上的畫面。
func iocsKeyinp(m *Machine) error {
	if m.Keys.Len() == 0 {
		return fmt.Errorf("_B_KEYINP：遊戲在等按鍵，但沒有鍵可送（用 -keys 給它）")
	}
	m.SetResult(uint32(m.Keys.Pop()))
	return nil
}
