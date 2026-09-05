package x68k

import "github.com/wicanr2/x68golem/m68k"

// 快照：把整台機器複製起來，之後可以回到那個狀態展開變體。
//
// **這是自己做一顆的實得好處之一**：MAME 對 `x68000` driver 標
// `savestate="unsupported"`（`docs/spec/001`）。有了快照，
// 「同一個盤面、換一個變數」從「重跑十七分鐘」變成「複製一份記憶體」。
//
// 沒有進快照的東西：`HotPC`、軌跡、`ServiceLog`、`Opens`——那些是診斷紀錄，
// 不是機器狀態。攔截點（hooks／intercepts）也不進：它們是觀測者的設定，
// 回到舊狀態不該把觀測者也一起換掉。
type Snapshot struct {
	cpu     m68k.State
	ram     []byte
	gvram   []byte
	tvram   []byte
	palette []byte
	latch   map[uint32]byte
	cgromOK bool

	steps, cycles uint64

	vdispHandler uint32
	vdispCount   uint32
	vdispPeriod  byte
	vdispNextAt  uint64
	callStack    []m68k.State

	dmacPending bool
	dmacVector  byte
	dmacDoneAt  uint64
	systemSSP   uint32

	crtc    CRTC
	console Console
	sprite  Sprite
	proc    human68kProcess
	vectors map[uint16]uint32

	crtMode   uint16
	vpage     byte
	screenUse map[byte]byte

	rng   RNG
	keys  Keyboard
	files map[uint16]openFile
}

// human68kProcess 是 Process 的值複本（Process 是指標，快照要複製內容）。
type human68kProcess struct {
	blockAddr, programEnd, blockEnd uint32
}

func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// Snapshot 複製目前的機器狀態。
func (m *Machine) Snapshot() *Snapshot {
	s := &Snapshot{
		cpu:          m.CPU.State,
		ram:          copyBytes(m.Bus.RAM),
		gvram:        copyBytes(m.Bus.GVRAM),
		tvram:        copyBytes(m.Bus.TVRAM),
		palette:      copyBytes(m.Bus.Palette),
		latch:        map[uint32]byte{},
		cgromOK:      m.Bus.CGROM != nil,
		steps:        m.steps,
		cycles:       m.cycles,
		vdispHandler: m.vdispHandler,
		vdispCount:   m.vdispCount,
		vdispPeriod:  m.vdispPeriod,
		vdispNextAt:  m.vdispNextAt,
		callStack:    append([]m68k.State(nil), m.callStack...),
		dmacPending:  m.dmacPending,
		dmacVector:   m.dmacVector,
		dmacDoneAt:   m.dmacDoneAt,
		systemSSP:    m.systemSSP,
		crtMode:      m.CRTMode,
		vpage:        m.VPage,
		screenUse:    map[byte]byte{},
		vectors:      map[uint16]uint32{},
		files:        map[uint16]openFile{},
	}
	for k, v := range m.Bus.latch {
		s.latch[k] = v
	}
	for k, v := range m.ScreenUse {
		s.screenUse[k] = v
	}
	for k, v := range m.Vectors {
		s.vectors[k] = v
	}
	for k, v := range m.files {
		f := *v
		f.data = v.data // 檔案內容唯讀，共用就好
		s.files[k] = f
	}
	if m.Bus.CRTC != nil {
		s.crtc = *m.Bus.CRTC
	}
	if m.Console != nil {
		s.console = *m.Console
		s.console.Out = copyBytes(m.Console.Out)
	}
	if m.Sprite != nil {
		s.sprite = *m.Sprite
	}
	if m.Process != nil {
		s.proc = human68kProcess{m.Process.BlockAddr, m.Process.ProgramEnd, m.Process.BlockEnd}
	}
	if m.RNG != nil {
		s.rng = *m.RNG
		s.rng.Seq = append([]uint32(nil), m.RNG.Seq...)
		s.rng.Log = append([]uint32(nil), m.RNG.Log...)
		s.rng.Seeds = append([]uint32(nil), m.RNG.Seeds...)
	}
	if m.Keys != nil {
		s.keys = *m.Keys
		s.keys.queue = append([]uint32(nil), m.Keys.queue...)
	}
	return s
}

// Restore 把機器回復到快照的狀態。
func (m *Machine) Restore(s *Snapshot) {
	m.CPU.State = s.cpu
	copy(m.Bus.RAM, s.ram)
	copy(m.Bus.GVRAM, s.gvram)
	copy(m.Bus.TVRAM, s.tvram)
	copy(m.Bus.Palette, s.palette)
	m.Bus.latch = map[uint32]byte{}
	for k, v := range s.latch {
		m.Bus.latch[k] = v
	}
	m.steps, m.cycles = s.steps, s.cycles
	m.Bus.Cycles = s.cycles
	m.vdispHandler, m.vdispCount = s.vdispHandler, s.vdispCount
	m.vdispPeriod, m.vdispNextAt = s.vdispPeriod, s.vdispNextAt
	m.callStack = append([]m68k.State(nil), s.callStack...)
	m.dmacPending, m.dmacVector, m.dmacDoneAt = s.dmacPending, s.dmacVector, s.dmacDoneAt
	m.systemSSP = s.systemSSP
	m.CRTMode, m.VPage = s.crtMode, s.vpage
	m.ScreenUse = map[byte]byte{}
	for k, v := range s.screenUse {
		m.ScreenUse[k] = v
	}
	m.Vectors = map[uint16]uint32{}
	for k, v := range s.vectors {
		m.Vectors[k] = v
	}
	m.files = map[uint16]*openFile{}
	for k, v := range s.files {
		f := v
		m.files[k] = &f
	}
	if m.Bus.CRTC != nil {
		*m.Bus.CRTC = s.crtc
	}
	if m.Console != nil {
		*m.Console = s.console
		m.Console.Out = copyBytes(s.console.Out)
	}
	if m.Sprite != nil {
		*m.Sprite = s.sprite
	}
	if m.Process != nil {
		m.Process.BlockAddr = s.proc.blockAddr
		m.Process.ProgramEnd = s.proc.programEnd
		m.Process.BlockEnd = s.proc.blockEnd
	}
	if m.RNG != nil {
		*m.RNG = s.rng
		m.RNG.Seq = append([]uint32(nil), s.rng.Seq...)
		m.RNG.Log = append([]uint32(nil), s.rng.Log...)
		m.RNG.Seeds = append([]uint32(nil), s.rng.Seeds...)
	}
	if m.Keys != nil {
		*m.Keys = s.keys
		m.Keys.queue = append([]uint32(nil), s.keys.queue...)
	}
}
