package x68k

import (
	"encoding/binary"
	"testing"

	"github.com/wicanr2/x68golem/internal/human68k"
)

// 攔截點要看得到「函式剛進入時」的狀態：參數還在堆疊上、返回位址在 (sp)。
func TestInterceptReplacesFunction(t *testing.T) {
	// 主程式：pea #$1234 ; jsr target ; addq.l #4,sp ; nop（停在這裡）
	// target：move.l #$DEAD,d0 ; rts   ← 應該完全不會執行到
	const base = 0x1000
	const target = 0x1020
	words := []uint16{
		0x4879, 0x0000, 0x1234, // pea ($1234).l
		0x4EB9, 0x0000, target, // jsr target
		0x588F,                 // addq.l #4,sp
		0x60FE,                 // bra.s *  ← 原地停住，否則會滑進填充的 nop 再撞到 target
	}
	// 補到 target 的位置
	for len(words) < (target-base)/2 {
		words = append(words, 0x4E71)
	}
	words = append(words,
		0x203C, 0x0000, 0xDEAD, // move.l #$0000DEAD,d0
		0x4E75,                 // rts
	)
	im, err := human68k.ParseZ(buildZ(base, words...))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMachine(im, DefaultRAMSize, "TEST.X")
	if err != nil {
		t.Fatal(err)
	}
	m.Bus.StrictIO = true

	var gotArg uint32
	calls := 0
	m.InstallIntercept(target, func(f *Frame) (uint32, bool) {
		calls++
		v, err := f.ArgLong(0)
		if err != nil {
			t.Error(err)
		}
		gotArg = v
		return 0xBEEF, true
	})

	for i := 0; i < 20; i++ {
		if err := m.Step(); err != nil {
			t.Fatalf("第 %d 步：%v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("攔截點被呼叫 %d 次", calls)
	}
	if gotArg != 0x1234 {
		t.Errorf("讀到的參數是 0x%X，應該是 0x1234", gotArg)
	}
	if m.CPU.State.D[0] != 0xBEEF {
		t.Errorf("D0 = 0x%X：原函式的 0xDEAD 跑掉了，或回傳值沒放進去", m.CPU.State.D[0])
	}
}

// 只看不改的攔截點不能影響執行。
func TestHookDoesNotChangeExecution(t *testing.T) {
	const base = 0x1000
	const target = 0x1020
	words := []uint16{
		0x4879, 0x0000, 0x1234,
		0x4EB9, 0x0000, target,
		0x588F, 0x60FE,
	}
	for len(words) < (target-base)/2 {
		words = append(words, 0x4E71)
	}
	words = append(words, 0x203C, 0x0000, 0xDEAD, 0x4E75)
	im, _ := human68k.ParseZ(buildZ(base, words...))
	m, _ := NewMachine(im, DefaultRAMSize, "TEST.X")
	m.Bus.StrictIO = true
	seen := 0
	m.InstallHook(target, func(f *Frame) { seen++ })
	for i := 0; i < 20; i++ {
		if err := m.Step(); err != nil {
			t.Fatalf("第 %d 步：%v", i, err)
		}
	}
	if seen != 1 {
		t.Fatalf("攔截點被呼叫 %d 次", seen)
	}
	if m.CPU.State.D[0] != 0xDEAD {
		t.Errorf("D0 = 0x%X：只看不改的攔截點不該影響原函式", m.CPU.State.D[0])
	}
}

// buildZ 在 machine_test.go；這裡只是確認它能造出夠長的映像。
var _ = binary.BigEndian
