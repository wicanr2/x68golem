package x68k

import (
	"image"
	"image/color"
)

// 把畫面重建成一張圖。
//
// **這一層只做「把記憶體翻譯成像素」，不做美化。** 驗收方式是與 MAME 的
// 索引截圖逐點比對（docs/spec/003）——拿自己畫的圖說自己對不算數。
//
// text 平面：四個 128 KB 的平面（`0xE00000`／`0xE20000`／`0xE40000`／
// `0xE60000`），每個平面 1 bit／像素、每列 1024 像素＝128 bytes。
// 四個平面合成 0–15 的色號，再查文字調色盤。
const (
	textPlaneStride = 0x20000
	textRowBytes    = 128
	textWidth       = 1024
	textHeight      = 1024

	// 文字調色盤在 `0xE82200`，16 個 word。
	textPaletteOffset = 0x200
)

// x68Color 把 X68000 的 16-bit 色值展開成 RGBA。
//
// 格式是 GGGGG RRRRR BBBBB I：綠、紅、藍各 5 bit，最低位是共用的亮度位。
// 展開時把亮度位接到每個色的最低位，再從 6 bit 擴到 8 bit。
func x68Color(v uint16) color.RGBA {
	i := v & 1
	g := (v>>11)&0x1F<<1 | i
	r := (v>>6)&0x1F<<1 | i
	b := (v>>1)&0x1F<<1 | i
	scale := func(c uint16) uint8 { return uint8(c<<2 | c>>4) }
	return color.RGBA{R: scale(r), G: scale(g), B: scale(b), A: 255}
}

// TextPalette 讀出 16 色的文字調色盤。
func (b *Bus) TextPalette() [16]color.RGBA {
	var pal [16]color.RGBA
	for i := 0; i < 16; i++ {
		off := textPaletteOffset + i*2
		v := uint16(b.Palette[off])<<8 | uint16(b.Palette[off+1])
		pal[i] = x68Color(v)
	}
	return pal
}

// TextIndices 回傳 w×h 的色號（0–15），左上角為原點。
// 回傳色號而不是顏色：**與 MAME 對拍時比的是索引，不是 RGB**
// ——索引不受調色盤設定與螢幕修正影響。
func (b *Bus) TextIndices(w, h int) []byte {
	if w > textWidth {
		w = textWidth
	}
	if h > textHeight {
		h = textHeight
	}
	out := make([]byte, w*h)
	for y := 0; y < h; y++ {
		rowBase := y * textRowBytes
		for x := 0; x < w; x++ {
			byteOff := rowBase + x/8
			bit := uint(7 - x%8)
			var v byte
			for p := 0; p < 4; p++ {
				if b.TVRAM[p*textPlaneStride+byteOff]>>bit&1 != 0 {
					v |= 1 << uint(p)
				}
			}
			out[y*w+x] = v
		}
	}
	return out
}

// TextImage 把文字平面畫成一張圖。
func (b *Bus) TextImage(w, h int) *image.RGBA {
	pal := b.TextPalette()
	idx := b.TextIndices(w, h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, pal[idx[y*w+x]])
		}
	}
	return img
}

// TextNonZero 回傳有幾個像素不是色號 0。空白畫面與有內容的畫面差在這裡，
// 而它是一個**可以斷言的數字**，不是「看起來有東西」。
func (b *Bus) TextNonZero(w, h int) int {
	n := 0
	for _, v := range b.TextIndices(w, h) {
		if v != 0 {
			n++
		}
	}
	return n
}
