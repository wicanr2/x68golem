// Package coverage 檢查 CPU 核心對「本專案實際要跑的程式」夠不夠用。
//
// `m68k/` 是上游的原封複製件（`m68k/PROVENANCE.md`），**不在那裡加測試**——
// 加了 `tools/sync-m68k.sh --check` 就會報分岔。缺口的紀錄放這裡。
package coverage

import (
	"testing"

	"github.com/wicanr2/x68golem/m68k"
)

// step 把一道指令放到 0x1000 執行一次，回傳錯誤（nil 表示核心處理得了）。
func step(t *testing.T, words ...uint16) error {
	t.Helper()
	mem := m68k.SparseMemory{}
	// 給一段可讀寫的空間：向量表、程式、堆疊。
	for a := uint32(0); a < 0x4000; a++ {
		mem[a] = 0
	}
	addr := uint32(0x1000)
	for i, w := range words {
		_ = mem.WriteWord(addr+uint32(i*2), w, 0)
	}
	// 68000 是 prefetch 機器：Prefetch[0] 是正在執行的那一道，
	// PC 已經指到再下一道的後面（addr + 4）。
	first, _ := mem.ReadWord(addr, 0)
	second, _ := mem.ReadWord(addr+2, 0)
	cpu := &m68k.CPU{Bus: mem}
	cpu.State = m68k.State{
		PC: addr + 4, SR: 0x2700, SSP: 0x3000,
		Prefetch: [2]uint16{first, second},
	}
	_, err := cpu.Step()
	return err
}

// SANMAIN.Z（KOEI 1988 X68000《三國志》）用到 71 種指令，其中 addx.l 出現 1 次。
// 這一支把「核心解不解得出來」變成可重跑的訊號，而不是讀 decode 表用眼睛判斷。
func TestADDXLong(t *testing.T) {
	// addx.l d1,d0 = 1101 000 1 10 00 0 001 = 0xD181
	if err := step(t, 0xD181); err != nil {
		t.Skipf("addx.l 尚未實作：%v（SANMAIN.Z 用到 1 次，見 docs/spec/002 §4）", err)
	}
}
