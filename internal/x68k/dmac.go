package x68k

// HD63450 DMAC——只做「開始傳送 → 立刻完成 → 發中斷」。
//
// 為什麼需要它：《三國志》開機到一半會停在
//
//	0x06A678  tst.l ($795D4).l
//	0x06A67E  beq.s -8            ← 等一個旗標變成非 0
//	0x06A680  jsr   $711A4        ← 讀 ($711A2) 的 byte
//	0x06A686  tst.l d0
//	0x06A688  bne.s -10           ← 等它變成 0
//
// 那個旗標由遊戲自己的 DMA 完成中斷處理常式清掉，而中斷來自 DMAC。
// 我們不發中斷，它就永遠等下去（`-hot` 顯示這三行各執行了兩百多萬次）。
//
// 通道 3（`0xE840C0` 起）在 X68000 上是 ADPCM。我們沒有音源，
// **所以不搬資料，只把「傳送完成」這件事做完**——對拍要的是時序與流程，
// 不是聲音。這一點寫在這裡，不要之後看到「DMAC 有了」就以為聲音會響。
const (
	dmacBase     = 0xE84000
	dmacChanSize = 0x40
	dmacChannels = 4

	dmacCSR = 0x00 // 狀態
	dmacCER = 0x01 // 錯誤
	dmacCCR = 0x07 // 控制（bit7 = 開始）
	dmacMTC = 0x0A // 記憶體傳送計數
	dmacNIV = 0x25 // 正常中斷向量編號

	dmacCSRComplete = 0x80 // COC：通道操作完成
)

// dmacWrite 處理寫進 DMAC 暫存器；回傳是否要在這一步之後發中斷。
func (m *Machine) dmacWrite(addr uint32, v byte) (irqVector byte, fire bool) {
	off := addr - dmacBase
	ch := int(off / dmacChanSize)
	reg := off % dmacChanSize
	if ch >= dmacChannels {
		return 0, false
	}
	base := dmacBase + uint32(ch)*dmacChanSize

	if reg == dmacCSR {
		// CSR 是「寫 1 清除」。
		m.Bus.latch[base+dmacCSR] &^= v
		return 0, false
	}
	m.Bus.latch[addr] = v
	if reg != dmacCCR || v&0x80 == 0 {
		return 0, false
	}
	// 開始位元被設起來：立刻宣告完成。
	m.Bus.latch[base+dmacCCR] = v &^ 0x80
	m.Bus.latch[base+dmacMTC] = 0
	m.Bus.latch[base+dmacMTC+1] = 0
	m.Bus.latch[base+dmacCSR] |= dmacCSRComplete
	m.DMACTransfers++
	return m.Bus.latch[base+dmacNIV], true
}

// serviceDMAC 在 DMAC 宣告完成之後，透過遊戲自己裝的向量發一次中斷。
func (m *Machine) serviceDMAC() (bool, error) {
	if !m.dmacPending || len(m.callStack) > 0 {
		return false, nil
	}
	m.dmacPending = false
	vec := uint32(m.dmacVector)
	if vec == 0 {
		return false, nil
	}
	handler, err := m.readLong(vec * 4)
	if err != nil {
		return false, err
	}
	if handler == 0 {
		return false, nil
	}
	if err := m.callInterrupt(handler); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Machine) writeLong(addr, v uint32) error {
	if err := m.Bus.WriteWord(addr, uint16(v>>16), 5); err != nil {
		return err
	}
	return m.Bus.WriteWord(addr+2, uint16(v), 5)
}

func (m *Machine) readLong(addr uint32) (uint32, error) {
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
