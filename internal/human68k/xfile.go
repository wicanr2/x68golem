package human68k

import (
	"encoding/binary"
	"fmt"
)

// `.X` 是 Human68k 的**可重定位**執行檔（`.Z` 是固定位址的平坦映像）。
//
// 檔頭 64 bytes：
//
//	$00 識別子 'HU'（0x4855）
//	$02 旗標
//	$04 基底位址（0 表示可重定位）
//	$08 執行起始位址
//	$0C text 長度
//	$10 data 長度
//	$14 bss 長度
//	$18 重定位表長度
//	$1C 符號表長度
//
// 檔案長度 = 64 + text + data + reloc + symbol。**對不上就當場失敗**。
const XHeaderSize = 64

// XImage 是一份讀好的 `.X`。
type XImage struct {
	Base     uint32
	Entry    uint32
	TextSize uint32
	DataSize uint32
	BSSSize  uint32
	Body     []byte // text ＋ data
	Reloc    []byte
}

// ParseX 讀一份 `.X`。
func ParseX(data []byte) (*XImage, error) {
	if len(data) < XHeaderSize {
		return nil, fmt.Errorf("human68k: `.X` 只有 %d bytes，連檔頭都不夠", len(data))
	}
	if magic := binary.BigEndian.Uint16(data[0:]); magic != 0x4855 {
		return nil, fmt.Errorf("human68k: `.X` 的識別子是 0x%04X，不是 'HU'", magic)
	}
	x := &XImage{
		Base:     binary.BigEndian.Uint32(data[0x04:]),
		Entry:    binary.BigEndian.Uint32(data[0x08:]),
		TextSize: binary.BigEndian.Uint32(data[0x0C:]),
		DataSize: binary.BigEndian.Uint32(data[0x10:]),
		BSSSize:  binary.BigEndian.Uint32(data[0x14:]),
	}
	relocSize := binary.BigEndian.Uint32(data[0x18:])
	symSize := binary.BigEndian.Uint32(data[0x1C:])
	want := uint64(XHeaderSize) + uint64(x.TextSize) + uint64(x.DataSize) +
		uint64(relocSize) + uint64(symSize)
	if uint64(len(data)) != want {
		return nil, fmt.Errorf(
			"human68k: 64 + text(%d) + data(%d) + reloc(%d) + symbol(%d) = %d，但檔案是 %d bytes",
			x.TextSize, x.DataSize, relocSize, symSize, want, len(data))
	}
	body := XHeaderSize + x.TextSize + x.DataSize
	x.Body = data[XHeaderSize:body]
	x.Reloc = data[body : body+relocSize]
	return x, nil
}

// Relocate 把映像搬到 loadAddr 之後要打的補丁套上去。
//
// 重定位表的編碼（照 `erique/ghidra-human68k` 的 `Human68kRelocation`，
// 那是從實作反推的，與我們手上的檔案能對上）：
//
//	讀一個 word d
//	  d == 0        → 結束
//	  d == 1        → 再讀一個 long 當位移，補一個 long
//	  d 是奇數      → 位移 = d − 1，補一個 word
//	  其他          → 位移 = d，補一個 long
//
// 位移是**累加**的，從映像起點算起。補丁的內容是「加上 loadAddr − Base」。
func (x *XImage) Relocate(mem []byte, loadAddr uint32) (int, error) {
	delta := loadAddr - x.Base
	if delta == 0 || len(x.Reloc) == 0 {
		return 0, nil
	}
	var off uint32
	n := 0
	for i := 0; i+1 < len(x.Reloc); {
		d := uint32(binary.BigEndian.Uint16(x.Reloc[i:]))
		i += 2
		var long bool
		switch {
		case d == 0:
			return n, nil
		case d == 1:
			if i+3 >= len(x.Reloc) {
				return n, fmt.Errorf("human68k: 重定位表在 32-bit 位移處截斷")
			}
			off += binary.BigEndian.Uint32(x.Reloc[i:])
			i += 4
			long = true
		case d&1 != 0:
			off += d - 1
		default:
			off += d
			long = true
		}
		if long {
			if int(off)+3 >= len(mem) {
				return n, fmt.Errorf("human68k: 重定位位移 0x%X 超出映像", off)
			}
			v := binary.BigEndian.Uint32(mem[off:]) + delta
			binary.BigEndian.PutUint32(mem[off:], v)
		} else {
			if int(off)+1 >= len(mem) {
				return n, fmt.Errorf("human68k: 重定位位移 0x%X 超出映像", off)
			}
			v := binary.BigEndian.Uint16(mem[off:]) + uint16(delta)
			binary.BigEndian.PutUint16(mem[off:], v)
		}
		n++
	}
	return n, nil
}
