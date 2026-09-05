package x68k

import (
	"encoding/binary"
	"fmt"

	"github.com/wicanr2/x68golem/internal/human68k"
	"github.com/wicanr2/x68golem/m68k"
)

// 載入並執行一支 `.X`（可重定位執行檔）。
//
// 為什麼需要：《三國志》的 `CONFIG.SYS` 載了 `\SYS\FLOAT2.X` 與
// `\SYS\OPMDRV.X`（`docs/findings/004`）。**遊戲的亂數就在 `FLOAT2.X` 裡**
// （`docs/findings/005`），所以要做出「與原版同一條亂數流」，
// 只有兩條路：把那支程式的演算法解出來，或者**把它載進來跑**。
//
// 這裡走第二條。風險不對稱：自己實作要能證明每一個運算逐位元相同，
// 那個證明的成本不見得比直接跑它低。

// LoadedDriver 是一支已經載好的驅動。
type LoadedDriver struct {
	Base  uint32
	Entry uint32
	Size  uint32
	Relocs int
}

// LoadX 把一份 `.X` 載到 base，套上重定位，回傳進入點。
func (m *Machine) LoadX(data []byte, base uint32) (*LoadedDriver, error) {
	x, err := human68k.ParseX(data)
	if err != nil {
		return nil, err
	}
	total := x.TextSize + x.DataSize + x.BSSSize
	if int(base+total) >= len(m.Bus.RAM) {
		return nil, fmt.Errorf("x68k: 驅動放不進主記憶體（基底 0x%X、長度 %d）", base, total)
	}
	copy(m.Bus.RAM[base:], x.Body)
	for i := base + uint32(len(x.Body)); i < base+total; i++ {
		m.Bus.RAM[i] = 0 // bss
	}
	n, err := x.Relocate(m.Bus.RAM[base:base+uint32(len(x.Body))], base)
	if err != nil {
		return nil, err
	}
	return &LoadedDriver{Base: base, Entry: base + (x.Entry - x.Base), Size: total, Relocs: n}, nil
}

// RunDriver 從進入點開始跑一支已經載好的驅動，直到它 `_KEEPPR`／`_EXIT`
// 或跑滿 maxSteps。
//
// 驅動跑完之後，機器的狀態（向量表、常駐記憶體）就是它裝好的樣子；
// 接著再載入主程式就會用到它。
func (m *Machine) RunDriver(d *LoadedDriver, maxSteps int) error {
	saved := m.CPU.State
	m.driverDone = false

	// 交棒契約與一般程式一樣，只是管理區塊放在驅動前面。
	if d.Base < human68k.ProcessBlockSize {
		return fmt.Errorf("x68k: 驅動基底 0x%X 太低，塞不下程式管理區塊", d.Base)
	}
	proc := &human68k.Process{
		BlockAddr:  d.Base - human68k.ProcessBlockSize,
		DataEnd:    d.Base + d.Size,
		ProgramEnd: d.Base + d.Size,
		BlockEnd:   d.Base + d.Size + 0x1000,
		Path:       "A:\\SYS\\",
		Name:       "DRIVER.X",
	}
	a0, a1, a2, a3 := proc.Layout(m.Bus.RAM)
	m.CPU.State = m68k.State{SR: 0x0000, USP: d.Base + d.Size + 0x800,
		SSP: uint32(len(m.Bus.RAM)) - 0x100}
	m.CPU.State.A[0], m.CPU.State.A[1] = a0, a1
	m.CPU.State.A[2], m.CPU.State.A[3] = a2, a3
	if err := m.resume(d.Entry); err != nil {
		return err
	}
	for i := 0; i < maxSteps; i++ {
		if m.driverDone {
			m.CPU.State = saved
			return nil
		}
		if err := m.Step(); err != nil {
			return fmt.Errorf("驅動第 %d 步：%w", i, err)
		}
	}
	return fmt.Errorf("驅動跑滿 %d 道指令還沒結束", maxSteps)
}

// LineFVector 讀目前 F-line 例外向量指到哪裡。
// 驅動裝好之後這個值會從我們的樁位址變成它自己的處理常式。
func (m *Machine) LineFVector() uint32 {
	return binary.BigEndian.Uint32(m.Bus.RAM[vectorLineF*4:])
}

// installDriverCalls 登記驅動結束時會用到的 DOS call。
func (m *Machine) installDriverCalls() {
	// $FF00 _EXIT／$FF4C _EXIT2：程式結束。
	m.DOSCalls[0x00] = func(mm *Machine) error { mm.driverDone = true; return nil }
	m.DOSCalls[0x4C] = func(mm *Machine) error { mm.driverDone = true; return nil }
	// $FF31 _KEEPPR：常駐並結束（TSR）。驅動裝好之後就走這裡。
	m.DOSCalls[0x31] = func(mm *Machine) error { mm.driverDone = true; return nil }
}
