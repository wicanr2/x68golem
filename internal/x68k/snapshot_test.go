package x68k

import (
	"testing"

	"github.com/wicanr2/x68golem/internal/human68k"
)

// 快照要能把「同一個狀態展開兩條路」做出來：從同一個快照跑兩次不同的步數，
// 回復之後狀態必須完全一致。
func TestSnapshotRestore(t *testing.T) {
	// moveq #0,d0 ; addq.l #1,d0 ; bra.s -4  ← 一直加
	m := newTestMachine(t, 0x7000, 0x5280, 0x60FC)
	for i := 0; i < 10; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	snap := m.Snapshot()
	d0 := m.CPU.State.D[0]
	steps := m.Steps()
	cycles := m.Cycles()

	for i := 0; i < 50; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if m.CPU.State.D[0] == d0 {
		t.Fatal("跑了 50 步 D0 沒變，這個測試本身就沒在測東西")
	}

	m.Restore(snap)
	if m.CPU.State.D[0] != d0 {
		t.Errorf("回復後 D0 = %d，應該是 %d", m.CPU.State.D[0], d0)
	}
	if m.Steps() != steps || m.Cycles() != cycles {
		t.Errorf("回復後步數／週期 = %d／%d，應該是 %d／%d",
			m.Steps(), m.Cycles(), steps, cycles)
	}

	// 從同一個快照再跑一次同樣的步數，結果必須一模一樣。
	for i := 0; i < 50; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	first := m.CPU.State
	m.Restore(snap)
	for i := 0; i < 50; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if m.CPU.State != first {
		t.Error("從同一個快照展開兩次，結果不一樣——機器不是決定性的")
	}
}

// 記憶體也要進快照，不只暫存器。
func TestSnapshotCoversMemory(t *testing.T) {
	// move.l #$1234,($3000).l ; bra.s *
	m := newTestMachine(t, 0x23FC, 0x0000, 0x1234, 0x0000, 0x3000, 0x60FE)
	snap := m.Snapshot()
	for i := 0; i < 3; i++ {
		if err := m.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := m.Bus.ReadWord(0x3002, 5); got != 0x1234 {
		t.Fatalf("寫入沒發生：0x%X", got)
	}
	m.Restore(snap)
	if got, _ := m.Bus.ReadWord(0x3002, 5); got != 0 {
		t.Errorf("回復後記憶體還留著 0x%X", got)
	}
}

var _ = human68k.ZHeaderSize
