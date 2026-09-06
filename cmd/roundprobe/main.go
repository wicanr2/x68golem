// Command roundprobe 試著在無頭執行器裡跑完原版戰場 AI 的**一整輪**
// （`sub_68382`），看它會不會停、停在哪。
//
// ⚠ 本專案不含任何原版檔案，`-z` 由玩家自備。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wicanr2/x68golem/apps/sangokushi"
	"github.com/wicanr2/x68golem/internal/x68k"
	"github.com/wicanr2/x68golem/oracle"
)

const (
	scratch  = 0x1E0000
	defSlots = scratch + 0x1900
	atkSlots = scratch + 0x1B00
	defGens  = scratch + 0x1D00
	atkGens  = scratch + 0x1F00
	nationB  = scratch + 0x2000
	cells    = sangokushi.BattleRows * sangokushi.TerrainCols
)

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	z := flag.String("z", "", "SANMAIN.Z（必填）")
	flag.Parse()
	o, err := oracle.Load(oracle.Config{Exe: *z, LatchIO: true})
	die(err)

	const defRuler, atkRuler = 3, 7
	var b sangokushi.Board
	b.Ruler = defRuler
	b.Terrain[5*14+6] = 0x20
	b.Terrain[5*14+7] = 0x20
	b.Terrain[8*14+2] |= 6
	put := func(slots, gens uint32, k int, ruler byte, x, y int) {
		g := gens + uint32(k)*0x30
		u := slots + uint32(k)*sangokushi.SlotStride
		die(sangokushi.General{Addr: g, Ruler: ruler, Intelligence: 50, Strength: 50}.Write(o))
		die(sangokushi.BattleUnit{Addr: u, General: g,
			X: uint32(x), Y: uint32(y), Troops: 5000}.Write(o))
		die(sangokushi.SetMobility(o, u, 6))
		b.Occupy[y*14+x] = u
	}
	for k := 0; k < 10; k++ {
		die(sangokushi.BattleUnit{Addr: defSlots + uint32(k)*sangokushi.SlotStride}.Write(o))
		die(sangokushi.BattleUnit{Addr: atkSlots + uint32(k)*sangokushi.SlotStride}.Write(o))
	}
	put(defSlots, defGens, 0, defRuler, 6, 5)
	put(defSlots, defGens, 1, defRuler, 9, 6)
	put(atkSlots, atkGens, 0, atkRuler, 2, 8)
	put(atkSlots, atkGens, 1, atkRuler, 3, 7)
	die(b.Write(o))

	for i := uint32(0); i < 0x100; i += 4 {
		die(o.SetLong(nationB+i, 0))
	}
	die(o.SetLong(sangokushi.NationPtr, nationB))
	die(o.SetLong(sangokushi.OppSlots, atkSlots))
	die(o.SetLong(sangokushi.DefRuler, defRuler))
	die(o.SetLong(sangokushi.AtkRuler, atkRuler))
	die(o.SetLong(sangokushi.ActingRuler, defRuler))
	die(o.SetLong(sangokushi.OppRuler, atkRuler))
	die(o.SetLong(sangokushi.ActingSlots, defSlots))
	die(o.SetLong(sangokushi.HQX, 2))
	die(o.SetLong(sangokushi.HQY, 8))
	die(o.SetLong(sangokushi.Wind, 3))
	die(o.SetLong(sangokushi.BattleDay, 4))
	die(o.SetLong(sangokushi.Difficulty, 1))
	die(sangokushi.ResetStepCost(o))
	die(sangokushi.BuildCastleList(o, scratch+0x1810, scratch+0x1880))

	for _, a := range []uint32{sangokushi.ActWindow, 0x5C042, 0x634AA, 0x61572} {
		o.Intercept(a, func(*x68k.Frame) (uint32, bool) { return 0, true })
	}
	var trace []string
	for a, nm := range map[uint32]string{
		sangokushi.ActStrike:   "strike",
		sangokushi.ActApproach: "move",
		sangokushi.ActRally:    "rally",
		sangokushi.ActFlank:    "flank",
		sangokushi.ActStandby:  "standby",
		0x676D4:                "evade",
		0x66FE6:                "retreat",
	} {
		nm := nm
		o.OnCall(a, func(*x68k.Frame) { trace = append(trace, nm) })
	}
	o.Intercept(sangokushi.ActStrike, func(*x68k.Frame) (uint32, bool) { return 0, true })

	start := o.Steps()
	if _, err := o.Call(sangokushi.PolicyTurn); err != nil {
		fmt.Println("跑不完：", err)
		fmt.Println("已走", o.Steps()-start, "道指令；trace =", trace)
		os.Exit(1)
	}
	fmt.Println("跑完，共", o.Steps()-start, "道指令")
	fmt.Println("trace =", trace)
	for k := 0; k < 4; k++ {
		base := uint32(defSlots)
		i := k
		if k >= 2 {
			base, i = atkSlots, k-2
		}
		u := base + uint32(i)*sangokushi.SlotStride
		x, _ := o.Long(u + 4)
		y, _ := o.Long(u + 8)
		m, _ := o.Byte(u + 0x10)
		fmt.Printf("槽 %d：(%d,%d) 機動 %d\n", k, int32(x), int32(y), m)
	}
}
