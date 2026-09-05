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

	services map[string]*Service
	order    []*Service
	steps    uint64

	// trace 是最近 N 道指令的環狀緩衝（PC ＋ 指令字）。
	// 停在一個沒實作的服務上時，「它是怎麼走到這裡的」比什麼都重要。
	trace    []TracePoint
	traceCap int
	traceN   int
}

// TracePoint 是軌跡上的一點。
type TracePoint struct {
	PC     uint32
	Opcode uint16
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

	// Human68k 的使用者程式在 user mode 起跑；crt0 自己會設 sp
	// （SANMAIN.Z 的第一件事就是 `lea ($8B468).l,sp`）。
	sup := uint32(ramSize - 0x100)
	m.CPU.State = m68k.State{SR: 0x0000, USP: im.BSSEnd() + 0x100, SSP: sup}
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
	m.Bus.PC = m.CPU.State.PC - 4
	if m.traceCap > 0 {
		m.trace[m.traceN%m.traceCap] = TracePoint{PC: m.Bus.PC, Opcode: m.CPU.State.Prefetch[0]}
		m.traceN++
	}
	if _, err := m.CPU.Step(); err != nil {
		return fmt.Errorf("PC=0x%06X：%w", m.Bus.PC, err)
	}
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
	s := m.note("DOS call", num, human68k.DOSCallName(op), pc)
	fn, ok := m.DOSCalls[num]
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
	_, pc, err := m.frame()
	if err != nil {
		return err
	}
	// trap 堆的是「下一道指令的位址」，直接回去就好。
	num := uint16(m.CPU.State.D[0] & 0xFF)
	s := m.note("IOCS", num, "", pc-2)
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
