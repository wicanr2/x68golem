package sangokushi_test

import (
	"encoding/binary"
	"testing"
)

// TestBusyWaitCycles 量 X68000 版 `W`（畫面等待閘門）那個空迴圈一圈幾個週期。
//
// `W` 的實作是逐指令讀出來的（`sangokushi_x68k_cht` 的 `docs/formats/06` §6.1，
// `dump_062342` `0x62648`）：
//
//	062648  move.l #$BB8,d1     ; 內層 3000
//	06264e  subq.l #1,d1
//	062650  bne.s  loc_6264E
//	062652  move.l d0,d1
//	062654  subq.l #1,d0
//	062656  tst.l  d1
//	062658  bne.s  loc_62648    ; 外層 T×2
//
// 指令序列是事實，**一圈幾個週期不是**——那要靠時序表或實測。這一支把
// 同一個內層迴圈組進記憶體再跑，用兩個不同圈數相減把呼叫的開銷消掉，
// 得到「一圈幾個週期」。有了它，`W` 的長度就是純算術：
// `T × 2 × (3000 × 一圈 + 外層開銷)`。
func TestBusyWaitCycles(t *testing.T) {
	o := loadExe(t)
	const at = 0x1E0000

	// move.l #n,d1 / subq.l #1,d1 / bne.s −4 / rts
	build := func(n uint32) {
		code := []uint16{0x223C, uint16(n >> 16), uint16(n), 0x5381, 0x66FC, 0x4E75}
		buf := make([]byte, 2)
		for i, w := range code {
			binary.BigEndian.PutUint16(buf, w)
			if err := o.SetByte(at+uint32(i)*2, buf[0]); err != nil {
				t.Fatal(err)
			}
			if err := o.SetByte(at+uint32(i)*2+1, buf[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	measure := func(n uint32) uint64 {
		build(n)
		c0 := o.Cycles()
		if _, err := o.Call(at); err != nil {
			t.Fatal(err)
		}
		return o.Cycles() - c0
	}

	a, b := measure(3000), measure(6000)
	per := float64(b-a) / 3000
	t.Logf("3000 圈 %d 週期、6000 圈 %d 週期 → 一圈 %.2f 週期", a, b, per)

	// 外層一圈 ＝ 內層 3000 圈 ＋ 幾道固定指令。
	outer := 3000*per + 38
	t.Logf("外層一圈 ≈ %.0f 週期；10 MHz 下 T=1 的 `W` ≈ %.2f ms",
		outer, 2*outer/10000)
	for _, tv := range []int{2, 3, 4, 5, 6, 7, 10, 12} {
		ms := float64(tv) * 2 * outer / 10000
		t.Logf("  T=%2d → %6.1f ms → 60 TPS 的 %d tick", tv, ms, int(ms/1000*60+0.5))
	}
}
