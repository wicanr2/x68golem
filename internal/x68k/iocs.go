package x68k

// IOCS（`trap #15`）：呼叫號在 D0，其餘參數在 D1／D2／A1…，回傳值在 D0。

// InstallIOCS 登記目前實作好的 IOCS 呼叫。
func (m *Machine) InstallIOCS() {
	m.IOCSCalls[0x0C] = iocsTvctrl
	m.IOCSCalls[0x0E] = iocsTgusemd
	m.IOCSCalls[0x10] = iocsCrtmod
	m.IOCSCalls[0x70] = iocsNoop // _MS_INIT：沒有滑鼠
	m.IOCSCalls[0x72] = iocsNoop // _MS_CUROF：沒有滑鼠游標
	m.IOCSCalls[0x7D] = iocsNoop // _SKEY_MOD：沒有軟體鍵盤
	m.IOCSCalls[0x81] = iocsSuper
	m.IOCSCalls[0x90] = iocsGClrOn
}

// iocsNoop 給「這台機器上沒有那個東西」的呼叫用：滑鼠、軟體鍵盤。
// 回 0 表示成功。**這不是偷懶的樁**——沒有滑鼠硬體時，
// 初始化滑鼠本來就沒有事情可做；有事情可做的呼叫不會走這裡。
func iocsNoop(m *Machine) error {
	m.SetResult(0)
	return nil
}

// iocsTvctrl 是 `$0C _TVCTRL`（d1.l 控制碼）：控制送給螢幕的訊號
//（同步、顯示開關那一類）。我們沒有真的螢幕電路，記下來就好。
func iocsTvctrl(m *Machine) error {
	m.TVControl = append(m.TVControl, m.CPU.State.D[1])
	m.SetResult(0)
	return nil
}

// iocsTgusemd 是 `$0E _TGUSEMD`（画面の使用状態の設定，d1.b 画面種別、
// d2.b 状態）。它只是登記「這個畫面現在算被誰用著」，沒有硬體副作用，
// 所以記下來就好。
func iocsTgusemd(m *Machine) error {
	if m.ScreenUse == nil {
		m.ScreenUse = map[byte]byte{}
	}
	m.ScreenUse[byte(m.CPU.State.D[1])] = byte(m.CPU.State.D[2])
	m.SetResult(0)
	return nil
}

// iocsCrtmod 是 `$10 _CRTMOD`（d1.w 畫面模式）：設定畫面模式，回傳舊的。
// d1 = −1 表示只問不改。
func iocsCrtmod(m *Machine) error {
	mode := uint16(m.CPU.State.D[1])
	old := uint32(m.CRTMode)
	if mode != 0xFFFF {
		m.CRTMode = mode
	}
	m.SetResult(old)
	return nil
}

// iocsGClrOn 是 `$90 _G_CLR_ON`：清掉並開啟圖形畫面。
// 我們把 graphics VRAM 清成 0；調色盤與顯示開關等 M3 接畫面時再說。
func iocsGClrOn(m *Machine) error {
	for i := range m.Bus.GVRAM {
		m.Bus.GVRAM[i] = 0
	}
	m.SetResult(0)
	return nil
}

// iocsSuper 是 IOCS `$81 _B_SUPER`：切換 supervisor／user 模式。
//
//	A1 = 0    → 進 supervisor，D0 回舊的 USP
//	A1 ≠ 0    → 回 user，USP 設成 A1，D0 回 0
//
// 判準（L2）：呼叫點是
//
//	0x070CF0  moveq    #$81,d0
//	0x070CF2  movea.l  #$00000000,a1
//	0x070CF8  trap     #15
//	0x070CFA  movea.l  d0,a1            ← 把回傳值收好，稍後再呼叫一次還原
//	0x070CFC  andi.b   #$F0,($00E8xxxx) ← 緊接著就開始寫硬體暫存器
//
// 「先要 supervisor、把舊值收好、然後才碰 I/O」這個形狀，配上 A1=0 的參數
// 與「回傳值會被原封不動地再傳回去」，就是 `_B_SUPER` 的用法。
func iocsSuper(m *Machine) error {
	const supervisorBit = 0x2000
	a1 := m.CPU.State.A[1]
	if a1 == 0 {
		old := m.CPU.State.USP
		m.CPU.State.SR |= supervisorBit
		m.SetResult(old)
		return nil
	}
	m.CPU.State.USP = a1
	m.CPU.State.SR &^= supervisorBit
	m.SetResult(0)
	return nil
}
