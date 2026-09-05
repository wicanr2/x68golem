// Package x68k 是 X68000 的機器層：記憶體配置、周邊區塊分類，
// 以及之後的畫面、CRTC、鍵盤。它不知道遊戲是什麼（docs/spec/006）。
package x68k

import "fmt"

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
	StrictIO bool

	// PC 由 Machine 在每一步之前更新，讓記帳知道是誰做的。
	PC uint32

	io    map[uint64]*IOAccess
	order []*IOAccess
}

func NewBus(ramSize int) *Bus {
	return &Bus{RAM: make([]byte, ramSize), io: map[uint64]*IOAccess{}}
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

func (b *Bus) inRAM(addr uint32) bool { return int(addr) < len(b.RAM) }

func (b *Bus) ReadByte(address uint32, _ uint8) (byte, error) {
	a := address & 0x00FFFFFF
	if b.inRAM(a) {
		return b.RAM[a], nil
	}
	b.note(a, false, 1)
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
	if b.inRAM(a) && b.inRAM(a+1) {
		return uint16(b.RAM[a])<<8 | uint16(b.RAM[a+1]), nil
	}
	b.note(a, false, 2)
	if b.StrictIO {
		return 0, fmt.Errorf("x68k: 讀 %s 0x%06X（PC=0x%06X）尚未實作", Classify(a), a, b.PC)
	}
	return 0, nil
}

func (b *Bus) WriteByte(address uint32, value byte, _ uint8) error {
	a := address & 0x00FFFFFF
	if b.inRAM(a) {
		b.RAM[a] = value
		return nil
	}
	b.note(a, true, 1)
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
	if b.inRAM(a) && b.inRAM(a+1) {
		b.RAM[a] = byte(value >> 8)
		b.RAM[a+1] = byte(value)
		return nil
	}
	b.note(a, true, 2)
	if b.StrictIO {
		return fmt.Errorf("x68k: 寫 %s 0x%06X（PC=0x%06X）尚未實作", Classify(a), a, b.PC)
	}
	return nil
}
