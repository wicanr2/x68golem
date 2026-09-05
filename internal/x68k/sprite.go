package x68k

// 精靈與 BG（PCG）。
//
// 遊戲全程走 IOCS（`$C0`–`$CF`），沒有直接寫 `$EB0000` 那一段，
// 所以這裡把狀態放在一般的 Go 結構裡，而不是模擬暫存器的位元組版面。
// **M3 要畫面時再決定要不要換成真的記憶體**——換的理由會是「有程式
// 直接讀寫那段位址」，不是「這樣比較像硬體」。
//
// 呼叫號與參數表來源：Data Crystal 的 X68000 IOCS 手冊整理（平台公開規格）。
type Sprite struct {
	// Patterns 是 PCG：256 個圖樣，16×16 4bpp ＝ 128 bytes（8×8 用前 32 bytes）。
	Patterns [256][128]byte
	// Regs 是 128 個精靈的暫存器。
	Regs [128]SpriteReg
	// BG 是兩層背景。
	BG [2]BGReg
	// BGText 是兩頁 64×64 的 BG 文字面。
	BGText [2][64 * 64]uint16
	// Palette 是精靈／BG 用的調色盤：16 個區塊 × 16 色。
	Palette [16][16]uint16
	// On 是精靈畫面的顯示開關。
	On bool
}

type SpriteReg struct {
	X, Y    uint16
	Pattern uint16
	Prio    uint8
}

type BGReg struct {
	ScrollX, ScrollY uint16
	TextPage         uint8
	Show             bool
}

// InstallSprite 登記精靈與 BG 的 IOCS 呼叫。
func (m *Machine) InstallSprite() {
	if m.Sprite == nil {
		m.Sprite = &Sprite{}
	}
	m.IOCSCalls[0xC0] = iocsSpInit
	m.IOCSCalls[0xC1] = func(mm *Machine) error { mm.Sprite.On = true; mm.SetResult(0); return nil }
	m.IOCSCalls[0xC2] = func(mm *Machine) error { mm.Sprite.On = false; mm.SetResult(0); return nil }
	m.IOCSCalls[0xC3] = iocsSpCgclr
	m.IOCSCalls[0xC4] = iocsSpDefcg
	m.IOCSCalls[0xC6] = iocsSpRegst
	m.IOCSCalls[0xC8] = iocsBgScrlst
	m.IOCSCalls[0xCA] = iocsBgCtrlst
	m.IOCSCalls[0xCC] = iocsBgTextcl
	m.IOCSCalls[0xCD] = iocsBgTextst
	m.IOCSCalls[0xCF] = iocsSpalet
}

// iocsSpInit 是 `$C0 _SP_INIT`：初始化精靈畫面。清掉所有圖樣與暫存器。
func iocsSpInit(m *Machine) error {
	*m.Sprite = Sprite{}
	m.SetResult(0)
	return nil
}

// iocsSpCgclr 是 `$C3 _SP_CGCLR`（d1.l 圖樣碼）：清掉一個圖樣。
func iocsSpCgclr(m *Machine) error {
	code := m.CPU.State.D[1] & 0xFF
	m.Sprite.Patterns[code] = [128]byte{}
	m.SetResult(0)
	return nil
}

// iocsSpDefcg 是 `$C4 _SP_DEFCG`（d1.l 圖樣碼、d2.l 尺寸、a1.l 資料）。
func iocsSpDefcg(m *Machine) error {
	code := m.CPU.State.D[1] & 0xFF
	n := uint32(32) // 8×8
	if m.CPU.State.D[2]&1 == 1 {
		n = 128 // 16×16
	}
	src := m.CPU.State.A[1]
	for i := uint32(0); i < n; i++ {
		b, err := m.Bus.ReadByte(src+i, 5)
		if err != nil {
			return err
		}
		m.Sprite.Patterns[code][i] = b
	}
	m.SetResult(0)
	return nil
}

// iocsSpRegst 是 `$C6 _SP_REGST`（d1 精靈編號、d2 X、d3 Y、d4 圖樣碼、d5 優先度）。
func iocsSpRegst(m *Machine) error {
	n := m.CPU.State.D[1] & 0x7F
	m.Sprite.Regs[n] = SpriteReg{
		X:       uint16(m.CPU.State.D[2]),
		Y:       uint16(m.CPU.State.D[3]),
		Pattern: uint16(m.CPU.State.D[4]),
		Prio:    uint8(m.CPU.State.D[5] & 3),
	}
	m.SetResult(0)
	return nil
}

// iocsBgScrlst 是 `$C8 _BGSCRLST`（d1 BG 編號、d2 X、d3 Y）。
func iocsBgScrlst(m *Machine) error {
	n := m.CPU.State.D[1] & 1
	m.Sprite.BG[n].ScrollX = uint16(m.CPU.State.D[2])
	m.Sprite.BG[n].ScrollY = uint16(m.CPU.State.D[3])
	m.SetResult(0)
	return nil
}

// iocsBgCtrlst 是 `$CA _BGCTRLST`（d1 BG 編號、d2 文字頁、d3 顯示旗標）。
func iocsBgCtrlst(m *Machine) error {
	n := m.CPU.State.D[1] & 1
	m.Sprite.BG[n].TextPage = uint8(m.CPU.State.D[2] & 1)
	m.Sprite.BG[n].Show = m.CPU.State.D[3] != 0
	m.SetResult(0)
	return nil
}

// iocsBgTextcl 是 `$CC _BGTEXTCL`（d1 文字頁、d2 圖樣碼）：整頁填同一個圖樣。
func iocsBgTextcl(m *Machine) error {
	p := m.CPU.State.D[1] & 1
	v := uint16(m.CPU.State.D[2])
	for i := range m.Sprite.BGText[p] {
		m.Sprite.BGText[p][i] = v
	}
	m.SetResult(0)
	return nil
}

// iocsBgTextst 是 `$CD _BGTEXTST`（d1 文字頁、d2 X、d3 Y、d4 圖樣碼）。
func iocsBgTextst(m *Machine) error {
	p := m.CPU.State.D[1] & 1
	x := m.CPU.State.D[2] & 63
	y := m.CPU.State.D[3] & 63
	m.Sprite.BGText[p][y*64+x] = uint16(m.CPU.State.D[4])
	m.SetResult(0)
	return nil
}

// iocsSpalet 是 `$CF _SPALET`（d1 調色盤碼、d2 區塊、d3 顏色碼）。
// d3 = −1 表示只問不改，回傳目前的顏色。
func iocsSpalet(m *Machine) error {
	code := m.CPU.State.D[1] & 0x0F
	block := m.CPU.State.D[2] & 0x0F
	old := uint32(m.Sprite.Palette[block][code])
	if int32(m.CPU.State.D[3]) != -1 {
		m.Sprite.Palette[block][code] = uint16(m.CPU.State.D[3])
	}
	m.SetResult(old)
	return nil
}
