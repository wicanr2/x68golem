package x68k

import "fmt"

// `$BC _PAINT`：把圖形畫面上一塊被指定顏色圍住的區域塗滿。
//
// 參數在 a1 指到的緩衝區（Data Crystal 的 IOCS 手冊）：
//
//	+0  word  X 座標
//	+2  word  Y 座標
//	+4  word  調色盤碼（**同時是邊界色與填色**）
//	+6  long  作業區起點
//	+10 long  作業區終點
//
// 語意是**種子填充**：記下起點的顏色，把「與起點同色」且相連的像素塗成
// 指定色，碰到任何別的顏色就停。
//
// 推論等級 **L1**：手冊那句「塗滿被指定色圍住的區域」同時符合種子填充與
// 邊界填充，而兩者在地圖上差了三個數量級——邊界填充每一次呼叫都塗滿整個
// 512×512。改成種子填充之後，27 次呼叫的結果讓圖形平面與 MAME
// 逐位元組相同（`docs/findings/021`、`022`）。
//
// 真機在作業區不夠大時會中途放棄；我們用自己的堆疊，不模擬那個上限。
// 這個差別**目前沒有出現**：把每一次呼叫用掉的掃描線段與堆疊深度印出來，
// 最深的一次是 904，而遊戲一律給 4096 bytes 的作業區（`docs/findings/021`）。
// 真的踩到上限時畫面會對不上，而對不上正是我們要看到的訊號。
func iocsPaint(m *Machine) error {
	p := m.CPU.State.A[1]
	x, err := m.Bus.ReadWord(p, 5)
	if err != nil {
		return err
	}
	y, err := m.Bus.ReadWord(p+2, 5)
	if err != nil {
		return err
	}
	col, err := m.Bus.ReadWord(p+4, 5)
	if err != nil {
		return err
	}
	// 256 色模式：調色盤碼是 8-bit，寫進 word 的低 byte（screen.go）。
	fill := byte(col)
	seed := byte(0)
	if sx, sy := int(int16(x)), int(int16(y)); sx >= 0 && sy >= 0 &&
		sx < graphicsWidth && sy < graphicsHeight {
		seed = m.Bus.GVRAM[(sy*graphicsWidth+sx)*2+1]
	}
	n := m.floodFill(int(int16(x)), int(int16(y)), fill)
	m.PaintPixels += n
	if len(m.PaintLog) < 200 {
		rl := func(a uint32) uint32 {
			hi, _ := m.Bus.ReadWord(a, 5)
			lo, _ := m.Bus.ReadWord(a+2, 5)
			return uint32(hi)<<16 | uint32(lo)
		}
		ws, we := rl(p+6), rl(p+10)
		m.PaintLog = append(m.PaintLog, fmt.Sprintf(
			"_PAINT (%d,%d) 色 0x%02X 起點色 0x%02X 作業區 %d bytes → 塗了 %d 個像素"+
				"（掃描線段 %d、堆疊最深 %d）",
			int16(x), int16(y), fill, seed, int(we)-int(ws), n,
			m.paintSegments, m.paintMaxDepth))
	}
	m.SetResult(0)
	return nil
}

// floodFill 是**種子填充**：把「與起點同色」且相連的像素塗成 fill，
// 碰到任何別的顏色就停。回傳塗了幾個像素。
//
// 這一版是量出來訂正的（`docs/findings/021`）。前一版做的是**邊界填充**
// （把所有「不是 fill」的相連像素塗成 fill），語意上是「邊界色＝填色」。
// 拿地圖實測，那個版本每一次呼叫都塗滿整個 512×512＝262,144 個像素，
// 最後一次 `色 0x00` 把整個平面洗成 0——真機在同一刻是 96,201 個非 0 像素。
// 地圖的州界是各種顏色的線，只有種子填充停得下來。
func (m *Machine) floodFill(x, y int, fill byte) int {
	if x < 0 || y < 0 || x >= graphicsWidth || y >= graphicsHeight {
		return 0
	}
	get := func(x, y int) byte { return m.Bus.GVRAM[(y*graphicsWidth+x)*2+1] }
	set := func(x, y int, c byte) { m.Bus.GVRAM[(y*graphicsWidth+x)*2+1] = c }
	seed := get(x, y)
	if seed == fill {
		return 0
	}
	painted := 0
	m.paintSegments, m.paintMaxDepth = 0, 0
	stack := [][2]int{{x, y}}
	for len(stack) > 0 {
		if len(stack) > m.paintMaxDepth {
			m.paintMaxDepth = len(stack)
		}
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		px, py := p[0], p[1]
		if py < 0 || py >= graphicsHeight || px < 0 || px >= graphicsWidth {
			continue
		}
		if get(px, py) != seed {
			continue
		}
		// 往左右各長到邊界。
		left := px
		for left > 0 && get(left-1, py) == seed {
			left--
		}
		right := px
		for right < graphicsWidth-1 && get(right+1, py) == seed {
			right++
		}
		m.paintSegments++
		for i := left; i <= right; i++ {
			set(i, py, fill)
			painted++
			if py > 0 && get(i, py-1) == seed {
				stack = append(stack, [2]int{i, py - 1})
			}
			if py < graphicsHeight-1 && get(i, py+1) == seed {
				stack = append(stack, [2]int{i, py + 1})
			}
		}
	}
	return painted
}
