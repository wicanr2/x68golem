package x68k

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
// 語意是**邊界填充**：從起點出發，把所有「不是那個顏色」的相連像素塗成
// 那個顏色，碰到那個顏色就停。
//
// 推論等級 **L3**：版面與語意取自平台手冊，**還沒有對過 MAME**。
// 遊戲畫地圖會用到它，等圖形平面能與 MAME 逐點比對時再驗
// （`docs/spec/003`；文字面已經比過，圖形面還沒有）。
//
// 真機在作業區不夠大時會中途放棄；我們用自己的堆疊，不模擬那個上限——
// 差別只在「原版會畫不完」的情況，那種情況出現時畫面會對不上，
// 而對不上正是我們要看到的訊號。
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
	page := int(m.VPage & 3)
	fill := byte(col & 0x0F)
	n := m.floodFill(page, int(int16(x)), int(int16(y)), fill)
	m.PaintPixels += n
	m.SetResult(0)
	return nil
}

// floodFill 用掃描線填充；回傳塗了幾個像素。
func (m *Machine) floodFill(page, x, y int, fill byte) int {
	if x < 0 || y < 0 || x >= graphicsWidth || y >= graphicsHeight {
		return 0
	}
	get := func(x, y int) byte {
		off := (y*graphicsWidth + x) * 2
		v := uint16(m.Bus.GVRAM[off])<<8 | uint16(m.Bus.GVRAM[off+1])
		return byte(v >> (uint(page) * 4) & 0x0F)
	}
	set := func(x, y int, c byte) {
		off := (y*graphicsWidth + x) * 2
		v := uint16(m.Bus.GVRAM[off])<<8 | uint16(m.Bus.GVRAM[off+1])
		shift := uint(page) * 4
		v = v&^(0x0F<<shift) | uint16(c)<<shift
		m.Bus.GVRAM[off] = byte(v >> 8)
		m.Bus.GVRAM[off+1] = byte(v)
	}
	if get(x, y) == fill {
		return 0
	}
	painted := 0
	stack := [][2]int{{x, y}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		px, py := p[0], p[1]
		if py < 0 || py >= graphicsHeight || px < 0 || px >= graphicsWidth {
			continue
		}
		if get(px, py) == fill {
			continue
		}
		// 往左右各長到邊界。
		left := px
		for left > 0 && get(left-1, py) != fill {
			left--
		}
		right := px
		for right < graphicsWidth-1 && get(right+1, py) != fill {
			right++
		}
		for i := left; i <= right; i++ {
			set(i, py, fill)
			painted++
			if py > 0 && get(i, py-1) != fill {
				stack = append(stack, [2]int{i, py - 1})
			}
			if py < graphicsHeight-1 && get(i, py+1) != fill {
				stack = append(stack, [2]int{i, py + 1})
			}
		}
	}
	return painted
}
