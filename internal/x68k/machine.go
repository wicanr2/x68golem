package x68k

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/wicanr2/x68golem/internal/human68k"
	"github.com/wicanr2/x68golem/m68k"
)

// 攔截用的兩個位址。放在主記憶體最上面，離 SANMAIN.Z 的 bss 結束
// （0x8B874）很遠。
//
// 為什麼用「例外向量指到一個樁位址」而不是「執行前偷看下一道指令」：
// 68000 是 prefetch 機器，自己動 PC 就要自己補 prefetch，很容易補錯而且
// 錯了不會報錯。走真正的例外流程，prefetch 由 CPU 自己處理，我們只負責
// 在樁位址上把服務做完再 RTE——這也正是 Human68k 實際的機制。
// supervisorStack 是留給 supervisor 堆疊的空間，程式拿不到。
const supervisorStack = 0x400

const (
	dosStub  = 0x1F0000
	iocsStub = 0x1F0010

	vectorLineF = 11        // F-line 模擬器例外
	vectorTrap15 = 32 + 15  // trap #15 ＝ IOCS
)

// Service 是一次服務呼叫的紀錄。
type Service struct {
	Kind   string // "DOS call" 或 "IOCS"
	Number uint16
	Name   string
	Count  int
	FirstPC uint32
	// Stubbed 表示這個服務至少被「回 0 混過去」一次（LenientServices）。
	Stubbed bool
}

// Machine 是一台可以跑 Human68k 執行檔的 X68000。
//
// 目前只有 CPU、記憶體與服務攔截；畫面、鍵盤、計時器都還沒有。
// **沒有的能力一律 fail-closed**——不假裝成功（docs/spec/004）。
type Machine struct {
	CPU *m68k.CPU
	Bus *Bus

	// DOSCalls／IOCS 是已實作的服務。沒登記的呼叫號會被記下來並停止執行。
	DOSCalls map[uint16]func(*Machine) error
	IOCSCalls map[uint16]func(*Machine) error

	// LenientServices 為真時，沒登記的服務回 D0=0 繼續跑，只記帳不停下。
	//
	// ⚠ **這樣跑出來的東西只能當「還有哪些服務存在」的線索，不能當事實。**
	// 程式會依服務的回傳值分支——回 0 等於替它選了一條路，而那條路多半是
	// 錯誤處理。用它得到的清單既可能少（走錯路而沒碰到）也可能多
	// （走進正常情況不會進的錯誤處理）。預設是 false。
	LenientServices bool

	// Process 是交棒給程式的那一組東西（A0–A3 指到的地方）。
	Process *human68k.Process

	// FloatCalls 是 FLOAT2.X 那一段（$FE00–$FEFF）；RNG 是受控的亂數來源。
	FloatCalls map[uint16]func(*Machine) error
	RNG        *RNG

	// Vectors 是程式用 _INTVCS 換掉的 Human68k 向量。
	Vectors map[uint16]uint32

	// Sprite 是精靈與 BG 的狀態。
	Sprite *Sprite

	// Drives 是軟碟機，依 PDA 低 4 位索引。原版磁碟由使用者自備。
	Drives []*Drive

	// Opens 記下每一次 _OPEN 的檔名與結果，這是「原版到底讀了什麼」的清單。
	Opens []string
	files map[uint16]*openFile

	// Console 是 Human68k 的主控台；Keyboard 之後接上去（M4）。
	Console  *Console
	Keyboard interface{ Pop() uint16 }

	// CRTMode 是 _CRTMOD 設的畫面模式；ScreenUse 是 _TGUSEMD 登記的使用狀態。
	CRTMode   uint16
	TVControl []uint32
	ScreenUse map[byte]byte

	services map[string]*Service
	order    []*Service
	steps    uint64
	cycles   uint64

	// trace 是最近 N 道指令的環狀緩衝（PC ＋ 指令字）。
	// 停在一個沒實作的服務上時，「它是怎麼走到這裡的」比什麼都重要。
	// 垂直同步中斷的狀態（vdisp.go）。
	vdispHandler uint32
	vdispCount   uint32
	vdispPeriod  byte
	vdispNextAt  uint64
	callStack    []m68k.State
	// VDispCalls 是垂直同步處理常式被叫過幾次。
	VDispCalls int

	// DMAC 的狀態（dmac.go）。
	dmacPending   bool
	dmacVector    byte
	DMACTransfers int

	// HotPC 統計每個位址被執行過幾次。開了才會統計。
	HotPC map[uint32]int

	// ServiceLog 為非 nil 時，每一次服務呼叫都寫一行進去。
	// 用來回答「這一連串服務裡，堆疊是在哪一步歪掉的」。
	ServiceLog func(line string)

	// systemSSP 是 _B_SUPER 進 supervisor 之前的系統堆疊指標。
	systemSSP uint32

	current  *Service
	trace    []TracePoint
	traceCap int
	traceN   int
}

// TracePoint 是軌跡上的一點。
//
// 只記指令字不夠用：`lea (d16,a0),a5` 的重點是那個 d16，而它在延伸字裡。
// 所以連後面兩個字一起記——不做反組譯，但把判讀需要的 bytes 留下來。
type TracePoint struct {
	PC    uint32
	Words [3]uint16
}

// NewMachine 建一台機器並把 image 載進去。
func NewMachine(im *human68k.Image, ramSize int) (*Machine, error) {
	if ramSize <= 0 {
		ramSize = DefaultRAMSize
	}
	if int(im.BSSEnd()) >= ramSize {
		return nil, fmt.Errorf("x68k: bss 結束在 0x%X，超出 %d bytes 的主記憶體", im.BSSEnd(), ramSize)
	}
	bus := NewBus(ramSize)
	copy(bus.RAM[im.Base:], im.Body) // text ＋ data
	// bss 是新配置的記憶體，Go 的 make 已經是 0；這裡不再多寫一次。

	m := &Machine{
		CPU:       &m68k.CPU{Bus: bus},
		Bus:       bus,
		DOSCalls:  map[uint16]func(*Machine) error{},
		IOCSCalls: map[uint16]func(*Machine) error{},
		services:  map[string]*Service{},
	}
	m.installVectors()
	bus.OnRegisterWrite = func(addr uint32, v byte) bool {
		if addr < dmacBase || addr >= dmacBase+dmacChannels*dmacChanSize {
			return false
		}
		vec, fire := m.dmacWrite(addr, v)
		if fire {
			m.dmacVector = vec
			m.dmacPending = true
		}
		return true
	}

	// Human68k 交棒契約：A0 指向記憶體管理標頭（＝載入基底 − 0x100），
	// A1 是記憶體結束，A2 是命令列，A3 是環境（internal/human68k/process.go）。
	if im.Base < human68k.ProcessBlockSize {
		return nil, fmt.Errorf("x68k: 載入基底 0x%X 太低，塞不下程式管理區塊", im.Base)
	}
	proc := &human68k.Process{
		BlockAddr:  im.Base - human68k.ProcessBlockSize,
		ProgramEnd: im.BSSEnd(),
		BlockEnd:   uint32(ramSize) - supervisorStack,
		Path:       "A:\\",
		Name:       "SANMAIN.Z",
	}
	a0, a1, a2, a3 := proc.Layout(bus.RAM)
	m.Process = proc

	// Human68k 的使用者程式在 user mode 起跑；crt0 自己會設 sp
	// （SANMAIN.Z 的第一件事就是 `lea ($8B468).l,sp`）。
	m.CPU.State = m68k.State{SR: 0x0000, USP: im.BSSEnd() + 0x100,
		SSP: uint32(ramSize) - 0x100}
	m.CPU.State.A[0] = a0
	m.CPU.State.A[1] = a1
	m.CPU.State.A[2] = a2
	m.CPU.State.A[3] = a3
	if err := m.resume(im.Entry); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Machine) installVectors() {
	put := func(vec int, target uint32) {
		binary.BigEndian.PutUint32(m.Bus.RAM[vec*4:], target)
	}
	put(vectorLineF, dosStub)
	put(vectorTrap15, iocsStub)
	// 樁位址本身放一個 NOP，讓例外進來時 prefetch 讀得到合法指令。
	binary.BigEndian.PutUint16(m.Bus.RAM[dosStub:], 0x4E71)
	binary.BigEndian.PutUint16(m.Bus.RAM[dosStub+2:], 0x4E71)
	binary.BigEndian.PutUint16(m.Bus.RAM[iocsStub:], 0x4E71)
	binary.BigEndian.PutUint16(m.Bus.RAM[iocsStub+2:], 0x4E71)
	binary.BigEndian.PutUint16(m.Bus.RAM[retStub:], 0x4E71)
	binary.BigEndian.PutUint16(m.Bus.RAM[retStub+2:], 0x4E71)
}

// resume 讓 CPU 從 pc 繼續：68000 的 PC 指到 prefetch 之後，
// 所以要同時把兩個 prefetch 字補好。
func (m *Machine) resume(pc uint32) error {
	w0, err := m.Bus.ReadWord(pc, 6)
	if err != nil {
		return err
	}
	w1, err := m.Bus.ReadWord(pc+2, 6)
	if err != nil {
		return err
	}
	m.CPU.State.PC = pc + 4
	m.CPU.State.Prefetch = [2]uint16{w0, w1}
	return nil
}

// MarkStubbed 讓服務實作自己承認「我只是回 0 混過去」，
// 報告會把它標出來。給探路用的樁呼叫。
func (m *Machine) MarkStubbed() {
	if m.current != nil {
		m.current.Stubbed = true
	}
}

// SetTraceDepth 打開指令軌跡（0 表示關掉）。
func (m *Machine) SetTraceDepth(n int) {
	m.traceCap = n
	m.trace = nil
	m.traceN = 0
	if n > 0 {
		m.trace = make([]TracePoint, n)
	}
}

// Trace 回傳最近的軌跡，最舊的在前面。
func (m *Machine) Trace() []TracePoint {
	if m.traceCap == 0 || m.traceN == 0 {
		return nil
	}
	if m.traceN < m.traceCap {
		return append([]TracePoint(nil), m.trace[:m.traceN]...)
	}
	i := m.traceN % m.traceCap
	return append(append([]TracePoint(nil), m.trace[i:]...), m.trace[:i]...)
}

// Steps 是已經執行的指令數。
func (m *Machine) Steps() uint64 { return m.steps }

// Cycles 是累計的 CPU 週期數。這是機器裡**唯一**的時間來源
// （docs/spec/002 §3：不讀主機時鐘）。
func (m *Machine) Cycles() uint64 { return m.cycles }

// Services 回傳所有碰到的服務呼叫，依第一次出現的順序。
func (m *Machine) Services() []*Service { return m.order }

func (m *Machine) note(kind string, num uint16, name string, pc uint32) *Service {
	key := fmt.Sprintf("%s/%d", kind, num)
	if s, ok := m.services[key]; ok {
		s.Count++
		return s
	}
	s := &Service{Kind: kind, Number: num, Name: name, Count: 1, FirstPC: pc}
	m.services[key] = s
	m.order = append(m.order, s)
	return s
}

// ErrUnimplemented 是「這個服務還沒做」。**這是正常的 M0 產出，不是 bug**——
// probe 就是靠它一層一層把清單挖出來。
type ErrUnimplemented struct {
	Service *Service
}

func (e *ErrUnimplemented) Error() string {
	return fmt.Sprintf("%s $%02X（%s）尚未實作，第一次出現在 PC=0x%06X",
		e.Service.Kind, e.Service.Number, e.Service.Name, e.Service.FirstPC)
}

// Step 執行一道指令，必要時先把停在樁位址上的服務做完。
func (m *Machine) Step() error {
	if handled, err := m.serviceStub(); err != nil {
		return err
	} else if handled {
		return nil
	}
	if handled, err := m.serviceDMAC(); err != nil {
		return err
	} else if handled {
		return nil
	}
	if handled, err := m.serviceVDisp(); err != nil {
		return err
	} else if handled {
		return nil
	}
	m.Bus.PC = m.CPU.State.PC - 4
	if m.HotPC != nil {
		m.HotPC[m.Bus.PC]++
	}
	if m.traceCap > 0 {
		tp := TracePoint{PC: m.Bus.PC}
		tp.Words[0] = m.CPU.State.Prefetch[0]
		tp.Words[1] = m.CPU.State.Prefetch[1]
		if w, err := m.Bus.ReadWord(m.Bus.PC+4, 6); err == nil {
			tp.Words[2] = w
		}
		m.trace[m.traceN%m.traceCap] = tp
		m.traceN++
	}
	res, err := m.CPU.Step()
	if err != nil {
		return fmt.Errorf("PC=0x%06X：%w", m.Bus.PC, err)
	}
	m.cycles += uint64(res.Clocks)
	m.Bus.Cycles = m.cycles
	m.steps++
	return nil
}

// serviceStub 檢查 CPU 是不是剛因為 DOS call／IOCS 例外跳到樁位址。
func (m *Machine) serviceStub() (bool, error) {
	cur := m.CPU.State.PC - 4
	switch cur {
	case dosStub:
		return true, m.serviceDOS()
	case iocsStub:
		return true, m.serviceIOCS()
	case retStub:
		return true, m.returnFromSub()
	}
	return false, nil
}

// frame 讀出 group 1／2 的例外堆疊框（SR、PC），並把 SSP 移回去。
func (m *Machine) frame() (sr uint16, pc uint32, err error) {
	ssp := m.CPU.State.SSP
	sr, err = m.Bus.ReadWord(ssp, 6)
	if err != nil {
		return
	}
	hi, err := m.Bus.ReadWord(ssp+2, 6)
	if err != nil {
		return
	}
	lo, err := m.Bus.ReadWord(ssp+4, 6)
	if err != nil {
		return
	}
	pc = uint32(hi)<<16 | uint32(lo)
	m.CPU.State.SSP = ssp + 6
	m.CPU.State.SR = sr
	return
}

func (m *Machine) serviceDOS() error {
	sr, pc, err := m.frame()
	if err != nil {
		return err
	}
	_ = sr
	// F-line 例外堆的是「那一道 F-line 指令自己的位址」，
	// 所以呼叫號要從那裡讀，回去的時候要跳過它（+2）。
	op, err := m.Bus.ReadWord(pc, 6)
	if err != nil {
		return err
	}
	num := op & 0x00FF
	kind := "DOS call"
	if human68k.IsFloatCall(op) {
		kind = "FLOAT2"
	} else if !human68k.IsDOSCall(op) {
		kind = "F-line"
	}
	s := m.note(kind, num, human68k.DOSCallName(op), pc)
	m.logService(kind, num, pc)
	m.current = s
	fn, ok := m.DOSCalls[num]
	if kind == "FLOAT2" {
		fn, ok = m.FloatCalls[num]
	} else if kind != "DOS call" {
		ok = false
	}
	if !ok {
		if !m.LenientServices {
			return &ErrUnimplemented{Service: s}
		}
		s.Stubbed = true
		m.CPU.State.D[0] = 0
		return m.resume(pc + 2)
	}
	if err := fn(m); err != nil {
		return err
	}
	return m.resume(pc + 2)
}

func (m *Machine) serviceIOCS() error {
	if m.ServiceLog != nil {
		st := m.CPU.State
		m.ServiceLog(fmt.Sprintf("  ↳進入前 SR=%04X USP=0x%06X SSP=0x%06X", st.SR, st.USP, st.SSP))
	}
	_, pc, err := m.frame()
	if err != nil {
		return err
	}
	// trap 堆的是「下一道指令的位址」，直接回去就好。
	num := uint16(m.CPU.State.D[0] & 0xFF)
	s := m.note("IOCS", num, IOCSName(num), pc-2)
	m.logService("IOCS", num, pc-2)
	m.current = s
	fn, ok := m.IOCSCalls[num]
	if !ok {
		if !m.LenientServices {
			return &ErrUnimplemented{Service: s}
		}
		s.Stubbed = true
		m.CPU.State.D[0] = 0
		return m.resume(pc)
	}
	if err := fn(m); err != nil {
		return err
	}
	return m.resume(pc)
}

func (m *Machine) logService(kind string, num uint16, pc uint32) {
	if m.ServiceLog == nil {
		return
	}
	st := m.CPU.State
	m.ServiceLog(fmt.Sprintf(
		"%-9s $%02X %-10s PC=0x%06X SR=%04X USP=0x%06X SSP=0x%06X D0=0x%08X D1=0x%08X A1=0x%08X",
		kind, num, IOCSName(num), pc, st.SR, st.USP, st.SSP, st.D[0], st.D[1], st.A[1]))
}

// SortedServices 回傳依種類與呼叫號排好的服務清單，給報告用。
func (m *Machine) SortedServices() []*Service {
	out := append([]*Service(nil), m.order...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Number < out[j].Number
	})
	return out
}
