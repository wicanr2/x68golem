// Package oracle 是給 remake 的 `go test` 用的觀測介面（`docs/spec/005`）。
//
// 契約刻意與 dosgolem 的 `oracle` 對齊，位址型別換成 68000 的線性位址
// ——`.Z` 是固定位址的平坦映像，IDA 的位址就是執行期位址，
// 所以這裡不提供任何換算介面（`docs/spec/006`）。
//
// **原版素材一律由呼叫端自備**：執行檔、磁碟映像、CGROM。
// 本套件不含也不會去找它們。
package oracle

import (
	"fmt"
	"os"

	"github.com/wicanr2/x68golem/internal/human68k"
	"github.com/wicanr2/x68golem/internal/x68k"
)

// Config 是開一台機器要的東西。
type Config struct {
	// Exe 是 Human68k 的 `.Z` 執行檔。
	Exe string
	// Disks 是軟碟映像（`.DIM`），依序放進 0 號、1 號磁碟機。
	Disks []string
	// CGROM 是字模。不給的話畫面上的字會是空白的。
	CGROM string
	// RAMSize 預設 2 MB。
	RAMSize int

	// Rand 決定亂數怎麼給。**沒有直通模式**：`FLOAT2.X` 的 `rand()` 演算法
	// 還沒解出來，任何「大概對」的亂數都會產生自洽但錯的對拍結論
	// （`docs/spec/005` §4）。
	Rand RandSource

	// KeyDelay 是兩個按鍵之間至少要隔多少 CPU 週期，預設 6,000,000。
	// **不能是 0**：開機時遊戲拿玩家的等待時間當亂數種子
	// （`docs/findings/008`）。
	KeyDelay uint64

	// LatchIO 把還沒實作的周邊暫存器當成單純的閂鎖。開機需要它。
	LatchIO bool
}

// RandSource 說亂數從哪裡來。
type RandSource struct {
	// Fixed 為非 nil 時，每次 `rand()` 都回同一個值。
	Fixed *uint32
	// Seq 為非空時，依序回這些值；用完就報錯，**不會退回真亂數**。
	Seq []uint32
}

// Oracle 是一台跑著原版的機器。
type Oracle struct {
	m *x68k.Machine
}

// Load 建一台機器並把執行檔載進去。
func Load(cfg Config) (*Oracle, error) {
	data, err := os.ReadFile(cfg.Exe)
	if err != nil {
		return nil, err
	}
	im, err := human68k.ParseZ(data)
	if err != nil {
		return nil, err
	}
	m, err := x68k.NewMachine(im, cfg.RAMSize)
	if err != nil {
		return nil, err
	}
	m.InstallDOSCalls()
	m.InstallIOCS()
	m.InstallConsole()
	m.InstallFDD()
	m.InstallFiles()
	m.InstallSprite()
	m.InstallFloat()
	m.InstallVDisp()
	m.InstallKeyboard()

	for _, p := range cfg.Disks {
		d, err := x68k.LoadDIM(p)
		if err != nil {
			return nil, err
		}
		m.Drives = append(m.Drives, d)
	}
	if cfg.CGROM != "" {
		rom, err := os.ReadFile(cfg.CGROM)
		if err != nil {
			return nil, err
		}
		m.Bus.CGROM = rom
	}
	m.Bus.LatchIO = cfg.LatchIO
	m.Bus.StrictIO = !cfg.LatchIO

	switch {
	case cfg.Rand.Fixed != nil:
		m.RNG.Mode = x68k.RNGFixed
		m.RNG.Value = *cfg.Rand.Fixed
	case len(cfg.Rand.Seq) > 0:
		m.RNG.Mode = x68k.RNGSeq
		m.RNG.Seq = cfg.Rand.Seq
	}

	delay := cfg.KeyDelay
	if delay == 0 {
		delay = 6_000_000
	}
	m.Keys.Delay = delay
	return &Oracle{m: m}, nil
}

// Machine 讓需要細節的呼叫端拿到底層機器。
// **這是逃生口，不是主要介面**——會用到它就表示 `oracle` 少了一個方法。
func (o *Oracle) Machine() *x68k.Machine { return o.m }

// Step 執行一道指令。
func (o *Oracle) Step() error { return o.m.Step() }

// Run 執行最多 n 道指令。
func (o *Oracle) Run(n int) error {
	for i := 0; i < n; i++ {
		if err := o.m.Step(); err != nil {
			return fmt.Errorf("第 %d 步（共 %d 道）：%w", i, o.m.Steps(), err)
		}
	}
	return nil
}

// RunUntil 一直跑到 cond 成立，最多 max 道指令。
// **跑滿了就回錯誤**，不要安靜地當成成功——那是「沉默不是成功」那條。
func (o *Oracle) RunUntil(cond func(*Oracle) bool, max int) error {
	for i := 0; i < max; i++ {
		if cond(o) {
			return nil
		}
		if err := o.m.Step(); err != nil {
			return err
		}
	}
	return fmt.Errorf("跑滿 %d 道指令，條件仍未成立", max)
}

// Keys 把一串字元排進鍵盤佇列。Return 用 "\r"。
func (o *Oracle) Keys(s string) { o.m.Keys.PushString(s) }

// Steps／Cycles 是已經執行的指令數與累計週期數。
func (o *Oracle) Steps() uint64  { return o.m.Steps() }
func (o *Oracle) Cycles() uint64 { return o.m.Cycles() }

// Byte／Word／Long 讀記憶體。
func (o *Oracle) Byte(addr uint32) (byte, error)   { return o.m.Bus.ReadByte(addr, 5) }
func (o *Oracle) Word(addr uint32) (uint16, error) { return o.m.Bus.ReadWord(addr, 5) }
func (o *Oracle) Long(addr uint32) (uint32, error) {
	hi, err := o.m.Bus.ReadWord(addr, 5)
	if err != nil {
		return 0, err
	}
	lo, err := o.m.Bus.ReadWord(addr+2, 5)
	if err != nil {
		return 0, err
	}
	return uint32(hi)<<16 | uint32(lo), nil
}

// TextIndices 回傳文字平面的色號（0–15）。
// **回索引不回 RGB**：與 MAME 對拍時比的是索引，不受調色盤設定影響。
func (o *Oracle) TextIndices(w, h int) []byte { return o.m.Bus.TextIndices(w, h) }

// TextPlane 回傳 text VRAM 平面 0 的原始 bytes（128 KB），
// 給與 MAME 的 dump 逐位元組比對用。
func (o *Oracle) TextPlane() []byte { return o.m.Bus.TVRAM[:0x20000] }

// OnCall 在 addr 上裝一個只看不改的攔截點。
func (o *Oracle) OnCall(addr uint32, fn func(*x68k.Frame)) { o.m.InstallHook(addr, fn) }

// Intercept 在 addr 上裝一個可以取代原函式的攔截點。
// fn 回傳 (回傳值, 是否略過原函式)。
func (o *Oracle) Intercept(addr uint32, fn func(*x68k.Frame) (uint32, bool)) {
	o.m.InstallIntercept(addr, fn)
}

// RandLog 是到目前為止每一次 `rand()` 回過的值。
// 錄一次再回放，就是「同一條亂數流、不同盤面」的交叉檢定。
func (o *Oracle) RandLog() []uint32 { return o.m.RNG.Log }

// Snapshot／Restore：把整台機器複製起來，之後回到那個狀態展開變體。
//
// MAME 對 `x68000` driver 標 `savestate="unsupported"`，所以這是自己做一顆
// 才有的能力。對拍時「同一個盤面、換一個變數」因此不必重跑一次開機。
func (o *Oracle) Snapshot() *x68k.Snapshot { return o.m.Snapshot() }
func (o *Oracle) Restore(s *x68k.Snapshot) { o.m.Restore(s) }
