// Package human68k 是 Human68k（X68000 的作業系統）那一層：執行檔格式、
// DOS call、以及之後的檔案服務。它不知道遊戲是什麼（docs/spec/006）。
package human68k

import (
	"encoding/binary"
	"fmt"
)

// ZHeaderSize 是 `.Z` 檔頭的長度。
const ZHeaderSize = 28

// Image 是一個載入好的 `.Z` 執行檔。
//
// `.Z` 是「在指定絕對位址執行」的平坦映像，**沒有重定位表**
// （`hcv -z<addr>` 產生的形式）。所以載入就是「照抄到 Base，bss 清 0」，
// 沒有任何位址換算——這也是本專案不提供換算介面的理由（docs/spec/006）。
type Image struct {
	Base     uint32 // text 段的第一個 byte 放在這個位址
	Entry    uint32 // 進入點（本格式即 Base）
	TextSize uint32
	DataSize uint32
	BSSSize  uint32
	Body     []byte // text ＋ data，長度 = TextSize + DataSize
}

// TextEnd／DataEnd／BSSEnd 是三個段的結束位址（不含）。
func (im *Image) TextEnd() uint32 { return im.Base + im.TextSize }
func (im *Image) DataEnd() uint32 { return im.TextEnd() + im.DataSize }
func (im *Image) BSSEnd() uint32  { return im.DataEnd() + im.BSSSize }

// ParseZ 讀一份 `.Z` 檔。
//
// 檔頭欄位（docs/spec/003，L0）：
//
//	$00 識別子 0x601A
//	$02 text 長度
//	$06 data 長度
//	$0A bss 長度
//	$0E 保留 8 bytes
//	$16 執行起始位址
//	$1A 識別子 0xFFFF
//
// **28 + text + data 必須等於檔案長度**；對不上就不是這個格式，當場失敗，
// 不要「盡量讀讀看」——讀錯的映像會在幾百萬個指令之後才以畫錯一格的形式出現。
func ParseZ(data []byte) (*Image, error) {
	if len(data) < ZHeaderSize {
		return nil, fmt.Errorf("human68k: 檔案只有 %d bytes，連檔頭都不夠", len(data))
	}
	if magic := binary.BigEndian.Uint16(data[0:]); magic != 0x601A {
		return nil, fmt.Errorf("human68k: $00 識別子是 0x%04X，不是 0x601A", magic)
	}
	if tail := binary.BigEndian.Uint16(data[0x1A:]); tail != 0xFFFF {
		return nil, fmt.Errorf("human68k: $1A 識別子是 0x%04X，不是 0xFFFF", tail)
	}
	im := &Image{
		TextSize: binary.BigEndian.Uint32(data[0x02:]),
		DataSize: binary.BigEndian.Uint32(data[0x06:]),
		BSSSize:  binary.BigEndian.Uint32(data[0x0A:]),
		Base:     binary.BigEndian.Uint32(data[0x16:]),
	}
	im.Entry = im.Base
	want := uint64(ZHeaderSize) + uint64(im.TextSize) + uint64(im.DataSize)
	if uint64(len(data)) != want {
		return nil, fmt.Errorf(
			"human68k: 28 + text(%d) + data(%d) = %d，但檔案是 %d bytes",
			im.TextSize, im.DataSize, want, len(data))
	}
	if im.Base&1 != 0 {
		return nil, fmt.Errorf("human68k: 載入基底 0x%X 是奇數位址", im.Base)
	}
	im.Body = data[ZHeaderSize:]
	return im, nil
}
