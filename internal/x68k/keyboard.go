package x68k

import "fmt"

// 鍵盤：IOCS `$00 _B_KEYINP`（等到有鍵為止）與 `$01 _B_KEYSNS`（不等）。
//
// 回傳值的低 byte 是字碼，高 byte 是掃描碼。目前只送字碼——
// 需要掃描碼的地方（方向鍵、功能鍵）等有實際用到再補，**不先編一份對照表**。
type Keyboard struct {
	queue []uint32

	// Delay 是兩個按鍵之間至少要隔多少 CPU 週期。
	//
	// **這不是為了好看，是因為遊戲會量玩家等了多久。** 開機第二個畫面是
	// 「乱数の初期化中です。少し待ってからリターンキーを押して下さい。」
	// ——原版拿玩家按下 Return 之前的等待時間當亂數種子。一次把整串鍵倒進去，
	// 等於零延遲，種子就固定了。
	Delay  uint64
	nextAt uint64
	cycles uint64
}

// Tick 讓鍵盤知道現在是第幾個週期。
func (k *Keyboard) Tick(cycles uint64) { k.cycles = cycles }

func (k *Keyboard) ready() bool { return k.cycles >= k.nextAt }

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
	if len(k.queue) == 0 || !k.ready() {
		return 0
	}
	return k.queue[0]
}

// Pop 取走一個；空的回 0。
func (k *Keyboard) Pop() uint16 {
	if len(k.queue) == 0 || !k.ready() {
		return 0
	}
	v := k.queue[0]
	k.queue = k.queue[1:]
	k.nextAt = k.cycles + k.Delay
	return uint16(v)
}

// Len 是還剩幾個鍵沒被取走。
func (k *Keyboard) Len() int { return len(k.queue) }

// InstallKeyboard 登記鍵盤相關的 IOCS。
func (m *Machine) InstallKeyboard() {
	if m.Keys == nil {
		m.Keys = &Keyboard{}
	}
	m.Keys.nextAt = m.Keys.Delay
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
