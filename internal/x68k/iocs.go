package x68k

// IOCS（`trap #15`）：呼叫號在 D0，其餘參數在 D1／D2／A1…，回傳值在 D0。

// InstallIOCS 登記目前實作好的 IOCS 呼叫。
func (m *Machine) InstallIOCS() {
	m.IOCSCalls[0x0C] = iocsTvctrl
	m.IOCSCalls[0x0E] = iocsTgusemd
	m.IOCSCalls[0x10] = iocsCrtmod
	// 滑鼠：這台機器上沒有。初始化、顯示／隱藏游標本來就沒有事情可做；
	// 讀狀態、讀移動量、讀按鍵時間一律回 0＝沒有動作、沒有按下。
	// **回 0 在這裡是真話**，不是樁。
	for _, n := range []uint16{0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79} {
		m.IOCSCalls[n] = iocsNoop
	}
	m.IOCSCalls[0x7D] = iocsNoop // _SKEY_MOD：沒有軟體鍵盤
	m.IOCSCalls[0x80] = iocsBIntvcs
	m.IOCSCalls[0x81] = iocsSuper
	m.IOCSCalls[0x90] = iocsGClrOn
	m.IOCSCalls[0x20] = iocsBPutc
	m.IOCSCalls[0x2E] = iocsBConsol
	m.IOCSCalls[0xB2] = iocsVpage
	m.IOCSCalls[0xBC] = iocsPaint
	m.IOCSCalls[0xF0] = iocsOpmdrv
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

// iocsBIntvcs 是 `$80 _B_INTVCS`（d1.w 向量編號、a1.l 新位址）：
// 換掉一個向量，回傳換掉之前的位址。
//
// **編號小於 0x100 的要真的寫進 68000 的例外向量表**，不是記在旁邊的表裡。
// 硬體中斷是照向量表跳的；記在旁邊的話，DMAC 完成中斷永遠找不到處理常式，
// 而遊戲會停在「等 PCM 播完」的迴圈裡不動（`docs/findings/007`）。
//
// 0x100 以上是 Human68k 自己的號碼（`_INTVCS` 那一段），沒有對應的
// 硬體向量，記在表裡就好。
func iocsBIntvcs(m *Machine) error {
	num := uint16(m.CPU.State.D[1])
	addr := m.CPU.State.A[1]
	if num < 0x100 {
		old, err := m.readLong(uint32(num) * 4)
		if err != nil {
			return err
		}
		if err := m.writeLong(uint32(num)*4, addr); err != nil {
			return err
		}
		m.SetResult(old)
		return nil
	}
	if m.Vectors == nil {
		m.Vectors = map[uint16]uint32{}
	}
	old := m.Vectors[num]
	m.Vectors[num] = addr
	m.SetResult(old)
	return nil
}

// iocsBPutc 是 `$20 _B_PUTC`（d1.w 字碼）：把一個字送到主控台。
// 與 `_CONCTRL` 模式 0 是同一件事，所以寫進同一個緩衝區。
func iocsBPutc(m *Machine) error {
	m.Console.putc(byte(m.CPU.State.D[1]))
	m.SetResult(0)
	return nil
}

// iocsBConsol 是 `$2E _B_CONSOL`（d1.l 起點、d2.l 範圍）：設定文字的顯示範圍。
// −1 表示不改。
func iocsBConsol(m *Machine) error {
	if int32(m.CPU.State.D[1]) != -1 {
		m.Console.RangeStart = m.CPU.State.D[1]
	}
	if int32(m.CPU.State.D[2]) != -1 {
		m.Console.RangeSize = m.CPU.State.D[2]
	}
	m.SetResult(0)
	return nil
}

// iocsVpage 是 `$B2 _VPAGE`（d1.b 頁面位元 0–3）：設定圖形畫面顯示哪幾頁。
// d1 = −1 表示只問不改。
func iocsVpage(m *Machine) error {
	old := uint32(m.VPage)
	if int32(m.CPU.State.D[1]) != -1 {
		m.VPage = byte(m.CPU.State.D[1] & 0x0F)
	}
	m.SetResult(old)
	return nil
}

// iocsOpmdrv 是 `$F0 _OPMDRV`（d1.l 功能號碼）：FM 音源驅動。
//
// 那是 `CONFIG.SYS` 載的 `OPMDRV.X` 提供的服務（`docs/findings/004`），
// 我們沒有音源，也**不在 MVP 範圍內**（`docs/spec/001`）。這裡把呼叫記下來
// 回 0：記下來是為了看得到「遊戲什麼時候要播哪一首」，那對對拍有用；
// 發聲沒有。
//
// ⚠ 如果將來發現遊戲會**等** OPMDRV 的回傳值才前進，這個回 0 就不夠了
// ——那時要的是實作，不是再加一個猜的值。
func iocsOpmdrv(m *Machine) error {
	m.OPMCalls = append(m.OPMCalls, m.CPU.State.D[1])
	m.SetResult(0)
	return nil
}

// iocsSuper 是 IOCS `$81 _B_SUPER`：切換 supervisor／user 模式。
//
//	A1 = 0，目前在 user      → **SSP ← USP**，進 supervisor，D0 回舊的 USP
//	A1 = 0，目前已在 supervisor → 什麼都不做，**D0 回 −1**
//	A1 ≠ 0                   → USP ← A1，回 user，D0 回 0
//
// 「已經在 supervisor 就回 −1」不是猜的，程式自己在檢查：
//
//	0x071048  movea.l (d16,pc),a1     ← 取回上次存的值
//	0x07104E  cmpa.l  #$FFFFFFFF,a1
//	0x071054  beq.w   跳過
//	0x071058  moveq   #$81,d0
//	0x07105A  trap    #15             ← 只有不是 −1 才還原
//
// 少了這一條，巢狀的 `_B_SUPER(0)` 會回傳一個過期的 USP，程式把它當成
// 「要還原的堆疊指標」存起來，離開時就把堆疊指標設歪 6 bytes——
// 然後 `rts` 彈出垃圾，PC 落在例外向量表本身（0x2C），
// 接著把向量表當成程式碼跑。
//
// **`SSP ← USP` 那一步是關鍵，不是細節。** 第一版只切了 S 位元，沒有把
// 堆疊指標搬過去，於是程式在 `_B_SUPER(0)` 之後的 `rts` 從一個完全不同的
// 堆疊上彈出東西——PC 變成 0x2C（例外向量表本身的位址），接著把向量表
// 當成程式碼執行。症狀離原因很遠，但成因只有一個：**程式以為自己還在同一個
// 堆疊上**，而 Human68k 的 `_B_SUPER` 保證了這件事。
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
		if m.CPU.State.SR&supervisorBit != 0 {
			m.SetResult(0xFFFFFFFF) // 已經在 supervisor
			return nil
		}
		old := m.CPU.State.USP
		// 系統的 supervisor 堆疊要留著：離開時要放回去，否則下一次
		// 從 user mode 進來的例外會把框推到程式自己的堆疊底下，
		// 一路蓋掉呼叫端還沒用到的返回位址。
		m.systemSSP = m.CPU.State.SSP
		m.CPU.State.SSP = old // 程式繼續用同一個堆疊
		m.CPU.State.SR |= supervisorBit
		m.SetResult(old)
		return nil
	}
	m.CPU.State.USP = a1
	if m.systemSSP != 0 {
		m.CPU.State.SSP = m.systemSSP
		m.systemSSP = 0
	}
	m.CPU.State.SR &^= supervisorBit
	m.SetResult(0)
	return nil
}
