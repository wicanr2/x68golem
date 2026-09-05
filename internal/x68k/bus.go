// Package x68k 是 X68000 的機器層：記憶體配置、周邊區塊分類，
// 以及之後的畫面、CRTC、鍵盤。它不知道遊戲是什麼（docs/spec/006）。
package x68k

import "fmt"

// VRAM 的位置與大小。這兩塊**是真的記憶體**，不是暫存器——
// 程式會直接讀寫它們，M3 的畫面也要從這裡取。
const (
	GVRAMBase = 0xC00000
	GVRAMSize = 0x200000 // 2 MB
	TVRAMBase = 0xE00000
	TVRAMSize = 0x080000 // 512 KB＝四個平面各 128 KB

	// 調色盤與視訊控制器：0xE82000 起。這一段是**讀得回來的暫存器檔**，
	// 不是只寫的埠——畫面要重建就得讀它，所以用真的記憶體backing。
	PaletteBase = 0xE82000
	PaletteSize = 0x000400 // 圖形 256 色 ＋ 文字 16 色 ＋ 視訊控制器
)

// DefaultRAMSize 是預設主記憶體大小（2 MB）。
// SANMAIN.Z 的 bss 結束在 0x8B874，遠小於這個值。
const DefaultRAMSize = 0x200000

// Region 是一段 I/O 位址的分類名稱。
type Region string

const (
	RegionRAM       Region = "主記憶體"
	RegionGVRAM     Region = "graphics VRAM"
	RegionTVRAM     Region = "text VRAM"
	RegionCRTC      Region = "CRTC"
	RegionVideoCtl  Region = "video controller ／ palette"
	RegionDMAC      Region = "DMAC"
	RegionMFP       Region = "MFP"
	RegionOPM       Region = "OPM（FM 音源）"
	RegionADPCM     Region = "ADPCM"
	RegionFDC       Region = "FDC"
	RegionPPI       Region = "i8255（joystick）"
	RegionSRAM      Region = "SRAM"
	RegionCGROM     Region = "CGROM"
	RegionIPLROM    Region = "IPL ROM"
	RegionUnknownIO Region = "未分類 I/O"
)

// Classify 把位址歸到一個區塊。
//
// 邊界取自 X68000 的公開記憶體配置；本專案只在 SANMAIN.Z 實際碰過的區塊上
// 做過交叉檢查（docs/spec/003）——那份普查**只抓得到絕對位址運算元，
// 是下界不是全部**，所以這裡把整個位址空間都分類，讓沒預期到的存取
// 也有名字可講，而不是掉進一個 default 分支裡。
func Classify(addr uint32) Region {
	a := addr & 0x00FFFFFF
	switch {
	case a < DefaultRAMSize:
		return RegionRAM
	case a >= 0xC00000 && a < 0xE00000:
		return RegionGVRAM
	case a >= 0xE00000 && a < 0xE80000:
		return RegionTVRAM
	case a >= 0xE80000 && a < 0xE82000:
		return RegionCRTC
	case a >= 0xE82000 && a < 0xE84000:
		return RegionVideoCtl
	case a >= 0xE84000 && a < 0xE86000:
		return RegionDMAC
	case a >= 0xE88000 && a < 0xE8A000:
		return RegionMFP
	case a >= 0xE8A000 && a < 0xE8C000:
		return RegionSRAM
	case a >= 0xE90000 && a < 0xE92000:
		return RegionOPM
	case a >= 0xE92000 && a < 0xE94000:
		return RegionADPCM
	case a >= 0xE94000 && a < 0xE96000:
		return RegionFDC
	case a >= 0xE9A000 && a < 0xE9C000:
		return RegionPPI
	case a >= 0xED0000 && a < 0xEE0000:
		return RegionSRAM
	case a >= 0xF00000 && a < 0xFC0000:
		return RegionCGROM
	case a >= 0xFE0000:
		return RegionIPLROM
	default:
		return RegionUnknownIO
	}
}

// IOAccess 記一次落在主記憶體以外的存取。
type IOAccess struct {
	Address uint32
	Region  Region
	Write   bool
	Size    int // 1 或 2 bytes
	PC      uint32
	Count   int
}

// Bus 是 24-bit 平坦位址空間：主記憶體是真的陣列，其餘一律記下來。
//
// **沒實作的周邊不假裝成功。** `StrictIO` 為真時，碰到主記憶體以外的位址
// 就回錯誤讓執行停下；為假時回 0／吞掉寫入，但仍然記帳——後者只給 probe 用，
// 而且 probe 會在報告裡講明「這一刻之後的紀錄不可信」。
type Bus struct {
	RAM      []byte
	GVRAM    []byte
	TVRAM    []byte
	Palette  []byte
	StrictIO bool

	// LatchIO：把還沒實作的周邊暫存器當成單純的閂鎖（寫什麼就讀得回什麼）。
	//
	// 這不是模擬硬體，是**讓「程式寫了自己再讀回來確認」這種等待迴圈走得完**。
	// SANMAIN.Z 有一段就是這樣：`ori.b #2,($E80481)` 之後
	// `btst #1,(a0)` / `beq` 等自己那一位變成 1；讀永遠回 0 就會卡死。
	// 真正的行為（什麼時候該由硬體清掉）要等實作 CRTC 時才有答案，
	// 在那之前這個模式讓執行走得下去，而報告仍然會把每一次存取記下來。
	LatchIO bool

	// PC 由 Machine 在每一步之前更新，讓記帳知道是誰做的。
	PC uint32

	// Watch 裡的位址每次被寫都會呼叫 OnWatch。主記憶體的寫入是熱路徑，
	// 所以只有在 Watch 非 nil 時才查。
	Watch   map[uint32]bool
	OnWatch func(addr uint32, v uint32, size int, pc uint32)

	// OnRegisterWrite 讓上層攔一個位元組寫入（DMAC 那類有副作用的暫存器）。
	// 回傳 true 表示已經處理掉了。
	OnRegisterWrite func(addr uint32, v byte) bool

	// StopOn 裡的位址一被碰到就回錯誤讓執行停下。
	// 用途是「我要看它是怎麼走到這個暫存器的」——停下來時軌跡還在。
	StopOn map[uint32]bool

	// CRTC 目前只實作動作埠（crtc.go）。
	CRTC *CRTC
	// Cycles 由 Machine 更新，讓需要時間的周邊有時間可用。
	Cycles uint64

	latch map[uint32]byte
	io    map[uint64]*IOAccess
	order []*IOAccess
}

func NewBus(ramSize int) *Bus {
	return &Bus{
		RAM:   make([]byte, ramSize),
		GVRAM: make([]byte, GVRAMSize),
		TVRAM:   make([]byte, TVRAMSize),
		Palette: make([]byte, PaletteSize),
		CRTC:  NewCRTC(),
		latch: map[uint32]byte{},
		io:    map[uint64]*IOAccess{},
	}
}

// resolve 把位址對到一塊真的記憶體。VRAM 是記憶體，暫存器不是。
// mainRAM 為真時不記帳——記帳是給「主記憶體以外」的報告用的。
func (b *Bus) resolve(addr uint32, size uint32) (mem []byte, off uint32, mainRAM, ok bool) {
	switch {
	case addr+size <= uint32(len(b.RAM)):
		return b.RAM, addr, true, true
	case addr >= GVRAMBase && addr+size <= GVRAMBase+GVRAMSize:
		return b.GVRAM, addr - GVRAMBase, false, true
	case addr >= TVRAMBase && addr+size <= TVRAMBase+TVRAMSize:
		return b.TVRAM, addr - TVRAMBase, false, true
	case addr >= PaletteBase && addr+size <= PaletteBase+PaletteSize:
		return b.Palette, addr - PaletteBase, false, true
	}
	return nil, 0, false, false
}

// IO 回傳所有記到的 I/O 存取，依第一次出現的順序。
func (b *Bus) IO() []*IOAccess { return b.order }

func (b *Bus) note(addr uint32, write bool, size int) {
	key := uint64(addr)<<8 | uint64(size)<<1
	if write {
		key |= 1
	}
	if a, ok := b.io[key]; ok {
		a.Count++
		return
	}
	a := &IOAccess{Address: addr, Region: Classify(addr), Write: write, Size: size, PC: b.PC, Count: 1}
	b.io[key] = a
	b.order = append(b.order, a)
}

// stop 回傳「這個位址是不是被 -stop-io 指名的」。
func (b *Bus) stop(addr uint32) error {
	if b.StopOn[addr] {
		return fmt.Errorf("x68k: 碰到指名的 I/O 位址 0x%06X（%s，PC=0x%06X）",
			addr, Classify(addr), b.PC)
	}
	return nil
}

func (b *Bus) ReadByte(address uint32, _ uint8) (byte, error) {
	a := address & 0x00FFFFFF
	if a == crtcOpPort && b.CRTC != nil {
		b.note(a, false, 1)
		if err := b.stop(a); err != nil {
			return 0, err
		}
		return b.CRTC.Read(b.Cycles), nil
	}
	if mem, off, mainRAM, ok := b.resolve(a, 1); ok {
		if !mainRAM {
			b.note(a, false, 1)
		}
		return mem[off], nil
	}
	b.note(a, false, 1)
	if err := b.stop(a); err != nil {
		return 0, err
	}
	if b.LatchIO {
		return b.latch[a], nil
	}
	if b.StrictIO {
		return 0, fmt.Errorf("x68k: 讀 %s 0x%06X（PC=0x%06X）尚未實作", Classify(a), a, b.PC)
	}
	return 0, nil
}

func (b *Bus) ReadWord(address uint32, _ uint8) (uint16, error) {
	a := address & 0x00FFFFFF
	if a&1 != 0 {
		return 0, fmt.Errorf("x68k: 奇數位址讀字 0x%06X（PC=0x%06X）", a, b.PC)
	}
	if mem, off, mainRAM, ok := b.resolve(a, 2); ok {
		if !mainRAM {
			b.note(a, false, 2)
		}
		return uint16(mem[off])<<8 | uint16(mem[off+1]), nil
	}
	b.note(a, false, 2)
	if err := b.stop(a); err != nil {
		return 0, err
	}
	if b.LatchIO {
		return uint16(b.latch[a])<<8 | uint16(b.latch[a+1]), nil
	}
	if b.StrictIO {
		return 0, fmt.Errorf("x68k: 讀 %s 0x%06X（PC=0x%06X）尚未實作", Classify(a), a, b.PC)
	}
	return 0, nil
}

func (b *Bus) WriteByte(address uint32, value byte, _ uint8) error {
	a := address & 0x00FFFFFF
	if a == crtcOpPort && b.CRTC != nil {
		b.note(a, true, 1)
		if err := b.stop(a); err != nil {
			return err
		}
		b.CRTC.Write(b.Cycles, value)
		return nil
	}
	if mem, off, mainRAM, ok := b.resolve(a, 1); ok {
		if !mainRAM {
			b.note(a, true, 1)
		}
		if b.Watch != nil && b.Watch[a] {
			b.OnWatch(a, uint32(value), 1, b.PC)
		}
		mem[off] = value
		return nil
	}
	b.note(a, true, 1)
	if err := b.stop(a); err != nil {
		return err
	}
	if b.OnRegisterWrite != nil && b.OnRegisterWrite(a, value) {
		return nil
	}
	if b.LatchIO {
		b.latch[a] = value
		return nil
	}
	if b.StrictIO {
		return fmt.Errorf("x68k: 寫 %s 0x%06X（PC=0x%06X）尚未實作", Classify(a), a, b.PC)
	}
	return nil
}

func (b *Bus) WriteWord(address uint32, value uint16, _ uint8) error {
	a := address & 0x00FFFFFF
	if a&1 != 0 {
		return fmt.Errorf("x68k: 奇數位址寫字 0x%06X（PC=0x%06X）", a, b.PC)
	}
	if mem, off, mainRAM, ok := b.resolve(a, 2); ok {
		if !mainRAM {
			b.note(a, true, 2)
		}
		if b.Watch != nil && (b.Watch[a] || b.Watch[a+1]) {
			b.OnWatch(a, uint32(value), 2, b.PC)
		}
		mem[off] = byte(value >> 8)
		mem[off+1] = byte(value)
		return nil
	}
	b.note(a, true, 2)
	if err := b.stop(a); err != nil {
		return err
	}
	if b.LatchIO {
		b.latch[a] = byte(value >> 8)
		b.latch[a+1] = byte(value)
		return nil
	}
	if b.StrictIO {
		return fmt.Errorf("x68k: 寫 %s 0x%06X（PC=0x%06X）尚未實作", Classify(a), a, b.PC)
	}
	return nil
}
