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

// ── 圖形平面 ─────────────────────────────────────────────────────────
//
// X68000 的 graphics VRAM 在 `0xC00000`，一個像素佔一個 word，
// 位址是 `0xC00000 + (y*512 + x) * 2`。**word 裡放什麼由畫面模式決定**。
//
// 《三國志》用 `_CRTMOD 8`，而那是 **256 色**——這是量出來的，不是查表：
// 把 MAME 在指令畫面 dump 的 512 KB 攤開，
//
//   - 高 byte **永遠是 0**（一種值）；
//   - 低 byte 有 55 種值，包含 255／191／128／113／105 這些大於 15 的。
//
// 所以一個像素是**低 byte 的 8-bit 調色盤索引**，不是「四頁各 4 bit」。
// 第一版照 16 色四頁做，結果我們的第 2、3 頁各有 137,600 個非 0 像素而
// MAME 是 0——那個對不上正是這一段被訂正的原因（`docs/findings/015`）。
const (
	graphicsWidth  = 512
	graphicsHeight = 512
)

// GraphicsIndices 回傳 256 色的調色盤索引。
//
// page 參數留著是為了將來的 16 色四頁模式；256 色模式下它沒有作用。
func (b *Bus) GraphicsIndices(page, w, h int) []byte {
	if w > graphicsWidth {
		w = graphicsWidth
	}
	if h > graphicsHeight {
		h = graphicsHeight
	}
	out := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*graphicsWidth + x) * 2
			if off+1 >= len(b.GVRAM) {
				continue
			}
			out[y*w+x] = b.GVRAM[off+1]
		}
	}
	return out
}

// GraphicsNonZero 回傳有幾個像素不是索引 0。
func (b *Bus) GraphicsNonZero(page, w, h int) int {
	n := 0
	for _, v := range b.GraphicsIndices(page, w, h) {
		if v != 0 {
			n++
		}
	}
	return n
}

// GraphicsPalette 讀出 256 色的圖形調色盤（`0xE82000` 起 256 個 word）。
func (b *Bus) GraphicsPalette() [256]color.RGBA {
	var pal [256]color.RGBA
	for i := 0; i < 256; i++ {
		if i*2+1 >= len(b.Palette) {
			break
		}
		v := uint16(b.Palette[i*2])<<8 | uint16(b.Palette[i*2+1])
		pal[i] = x68Color(v)
	}
	return pal
}

// GraphicsImage 把圖形畫面畫成一張圖。
func (b *Bus) GraphicsImage(page, w, h int) *image.RGBA {
	pal := b.GraphicsPalette()
	idx := b.GraphicsIndices(page, w, h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, pal[idx[y*w+x]])
		}
	}
	return img
}
