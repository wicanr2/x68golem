package x68k

import "fmt"

// Human68k 的主控台（`_CONCTRL`／`_INPOUT`）。
//
// 這個遊戲的畫面不走主控台——它直接寫 text VRAM 與圖形平面。主控台在
// 開機階段只被用來設幾個模式（關掉功能鍵列、選畫面解析度、關游標）。
// 所以這裡把輸出**記下來**而不畫出來：記下來的東西是診斷用的，
// 畫面由 M3 從 VRAM 取。
//
// 模式表來源：Data Crystal 的 Human68k DOSCALL 手冊整理
// （https://datacrystal.tcrf.net/wiki/X68k/DOSCALL）——平台公開規格。
type Console struct {
	Out       []byte // 主控台輸出（診斷用）
	Attr      uint16
	CursorX   uint16
	CursorY   uint16
	FnKeyMode uint16 // _CONCTRL 模式 14
	ScreenMod uint16 // _CONCTRL 模式 16
	ScrollTop uint16 // _CONCTRL 模式 15
	ScrollLen uint16
	CursorOn  bool
	Clears    int // 清畫面的次數
}

func (c *Console) putc(b byte) { c.Out = append(c.Out, b) }

// InstallConsole 登記 `_CONCTRL` 與 `_INPOUT`。
func (m *Machine) InstallConsole() {
	if m.Console == nil {
		m.Console = &Console{}
	}
	m.DOSCalls[0x06] = dosInpout
	m.DOSCalls[0x23] = dosConctrl
}

// dosInpout 是 `$FF06 _INPOUT(碼.w)`：
//
//	$FF → 不等待的鍵盤輸入，沒有按鍵就回 0
//	$FE → 緩衝鍵盤輸入
//	其他 → 當成字元碼輸出
//
// 鍵盤還沒接（M4），所以兩種輸入都回 0＝沒有按鍵。**回 0 在這裡是真話**，
// 不是樁：沒有人按鍵的時候，原版也是回 0。
func dosInpout(m *Machine) error {
	code, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	switch code {
	case 0xFF, 0xFE:
		if m.Keyboard != nil {
			m.SetResult(uint32(m.Keyboard.Pop()))
			return nil
		}
		m.SetResult(0)
	default:
		m.Console.putc(byte(code))
		m.SetResult(0)
	}
	return nil
}

// dosConctrl 是 `$FF23 _CONCTRL(模式.w, …)`。
//
// **沒實作的模式回錯誤，不回 0。** 主控台的模式各自有不同的參數長度，
// 猜錯會把堆疊讀歪，而那種錯不會當場爆——會在很久以後變成一個畫錯的字。
func dosConctrl(m *Machine) error {
	mode, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	c := m.Console
	switch mode {
	case 0: // 顯示一個 byte
		code, err := m.ArgWord(2)
		if err != nil {
			return err
		}
		c.putc(byte(code))
	case 1: // 顯示字串
		ptr, err := m.ArgLongAt(2)
		if err != nil {
			return err
		}
		for i := uint32(0); i < 4096; i++ {
			b, err := m.Bus.ReadByte(ptr+i, 5)
			if err != nil {
				return err
			}
			if b == 0 {
				break
			}
			c.putc(b)
		}
	case 2: // 設定字元屬性
		v, err := m.ArgWord(2)
		if err != nil {
			return err
		}
		c.Attr = v
	case 3: // 游標定位
		x, err := m.ArgWord(2)
		if err != nil {
			return err
		}
		y, err := m.ArgWord(4)
		if err != nil {
			return err
		}
		c.CursorX, c.CursorY = x, y
	case 10, 11: // 清畫面／清一列
		c.Clears++
	case 15: // 設定捲動範圍（YS 起點、YL 列數）
		ys, err := m.ArgWord(2)
		if err != nil {
			return err
		}
		yl, err := m.ArgWord(4)
		if err != nil {
			return err
		}
		c.ScrollTop, c.ScrollLen = ys, yl
	case 14: // 功能鍵列的顯示模式，回傳舊的
		v, err := m.ArgWord(2)
		if err != nil {
			return err
		}
		old := uint32(c.FnKeyMode)
		if v != 0xFFFF {
			c.FnKeyMode = v
		}
		m.SetResult(old)
		return nil
	case 16: // 畫面解析度／圖形模式，回傳舊的
		v, err := m.ArgWord(2)
		if err != nil {
			return err
		}
		old := uint32(c.ScreenMod)
		if v != 0xFFFF {
			c.ScreenMod = v
		}
		m.SetResult(old)
		return nil
	case 17:
		c.CursorOn = true
	case 18:
		c.CursorOn = false
	default:
		return fmt.Errorf("_CONCTRL：模式 %d 還沒實作", mode)
	}
	m.SetResult(0)
	return nil
}
