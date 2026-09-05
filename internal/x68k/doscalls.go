package x68k

import "fmt"

// Human68k 的 DOS call 用**堆疊傳參數**，呼叫端自己收拾
// （`$FF4A` 後面緊接著 `addq.l #8,sp`），回傳值放 D0。
//
// 服務執行的當下我們已經把例外堆疊框收掉了，所以 SP 就是呼叫端推參數
// 之前的那個 SP——第一個參數在 SP+0。

// SP 回傳目前有效的堆疊指標。user mode 用 USP，supervisor mode 用 SSP。
func (m *Machine) SP() uint32 {
	if m.CPU.State.SR&0x2000 != 0 {
		return m.CPU.State.SSP
	}
	return m.CPU.State.USP
}

// ArgWord 讀堆疊上偏移 off 處的 word 參數。
func (m *Machine) ArgWord(off uint32) (uint16, error) {
	return m.Bus.ReadWord(m.SP()+off, 5)
}

// ArgLongAt 讀堆疊上偏移 off 處的 long 參數。
func (m *Machine) ArgLongAt(off uint32) (uint32, error) {
	hi, err := m.Bus.ReadWord(m.SP()+off, 5)
	if err != nil {
		return 0, err
	}
	lo, err := m.Bus.ReadWord(m.SP()+off+2, 5)
	if err != nil {
		return 0, err
	}
	return uint32(hi)<<16 | uint32(lo), nil
}

// ArgLong 讀第 n 個 long 參數（從 0 起算）。
func (m *Machine) ArgLong(n int) (uint32, error) {
	addr := m.SP() + uint32(n*4)
	hi, err := m.Bus.ReadWord(addr, 5)
	if err != nil {
		return 0, err
	}
	lo, err := m.Bus.ReadWord(addr+2, 5)
	if err != nil {
		return 0, err
	}
	return uint32(hi)<<16 | uint32(lo), nil
}

// SetResult 把回傳值放進 D0。
func (m *Machine) SetResult(v uint32) { m.CPU.State.D[0] = v }

// InstallDOSCalls 登記目前實作好的 DOS call。
func (m *Machine) InstallDOSCalls() {
	m.DOSCalls[0x25] = dosIntvcs
	m.DOSCalls[0x4A] = dosSetblock
}

// dosIntvcs 是 `$FF25 _INTVCS(向量編號 word, 新位址 long)`：換掉一個
// 中斷／例外向量，回傳**換掉之前**的位址。
//
// 參數形狀是量出來的（L2）：
//
//	0x06E8FC  pea    (d16,pc)        ← 先推 long
//	0x06E902  move.w #$FFF1,-(sp)    ← 再推 word
//	0x06E906  $FF25
//	0x06E908  addq.l #6,sp           ← 呼叫端收 6 bytes ＝ 2 + 4
//
// 所以堆疊上是「word 在前、long 在後」。編號 0xFFxx 那一段是 Human68k
// 自己的向量，不是 68000 的例外向量表。
//
// 我們只記在一張表上，還沒有任何東西會去觸發它們（L3：回傳的舊位址
// 一律是 0，真機上會是 Human68k 自己的處理常式位址）。
func dosIntvcs(m *Machine) error {
	num, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	addr, err := m.ArgLongAt(2)
	if err != nil {
		return err
	}
	if m.Vectors == nil {
		m.Vectors = map[uint16]uint32{}
	}
	old := m.Vectors[num]
	m.Vectors[num] = addr
	m.SetResult(old)
	return nil
}

// dosSetblock 是 `$FF4A _SETBLOCK(區塊位址, 新長度)`：把已經配到的記憶體
// 區塊改成指定長度。crt0 用它把多要的還回去。
//
// 名稱與參數形狀是從 crt0 的行為推的（L2，`docs/findings/001`）：
// 兩個 long 推堆疊，長度由「結束 − 起點」算出來。
//
// 我們的記憶體是一整塊平的，沒有配置器，所以這裡只做三件事：
// 確認位址是我們發出去的那一塊、確認新長度放得下、記下新的結束位址。
// **放不下就回錯誤碼，不是回成功**——Human68k 失敗時回負值。
func dosSetblock(m *Machine) error {
	addr, err := m.ArgLong(0)
	if err != nil {
		return err
	}
	length, err := m.ArgLong(1)
	if err != nil {
		return err
	}
	want := m.Process.BlockAddr + 0x10
	if addr != want {
		return fmt.Errorf("_SETBLOCK：區塊位址 0x%06X 不是我們發出去的 0x%06X", addr, want)
	}
	end := uint64(addr) + uint64(length)
	if end > uint64(len(m.Bus.RAM)) {
		m.SetResult(0xFFFFFFF8) // Human68k 的「記憶體不足」是負值
		return nil
	}
	m.Process.BlockEnd = uint32(end)
	m.SetResult(0)
	return nil
}
