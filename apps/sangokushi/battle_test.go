package sangokushi_test

import (
	"os"
	"testing"

	"github.com/wicanr2/x68golem/apps/sangokushi"
	"github.com/wicanr2/x68golem/oracle"
)

// 這一支要的只有執行檔——**不用磁碟、不用 CGROM、不用開機**。
//
// `.Z` 是固定位址的平坦映像，公式要讀的表（`0x7757A`）在 data 段裡，
// 載入當下就是對的。所以「問原版一條公式」的成本是幾毫秒，不是幾十秒。
func loadExe(t *testing.T) *oracle.Oracle {
	t.Helper()
	z := os.Getenv("X68GOLEM_TEST_Z")
	if z == "" {
		t.Skip("X68GOLEM_TEST_Z 沒設")
	}
	o, err := oracle.Load(oracle.Config{Exe: z, LatchIO: true})
	if err != nil {
		t.Skipf("開不起來：%v", err)
	}
	return o
}

// 合成盤面的位址：主記憶體 2 MB，遊戲的堆積到 0xFCA86 為止，
// 樁在 0x1F0000 起。0x1E0000 這一帶誰都不會碰。
const (
	scratch  = 0x1E0000
	unitA    = scratch + 0x000
	unitD    = scratch + 0x040
	generalA = scratch + 0x080
	generalD = scratch + 0x0C0
)

// setupAttack 擺一個「弱者打強者」的盤面，回傳雙方的初始兵數。
//
// 兩邊的**君主編號都給 0**，所以 `sub_654D2` 的補正對雙方相同，
// 在 `戰力(X) − 戰力(Y)` 裡抵銷——這條測試因此與難度設定無關。
func setupAttack(t *testing.T, o *oracle.Oracle) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// 知力給 0：`sub_65530` 的「知力 ≥ 96 就這次不損兵」那條路不會走到，
	// 公式因此只剩戰力差與亂數項。
	must(sangokushi.General{Addr: generalA, Strength: 10}.Write(o))
	must(sangokushi.General{Addr: generalD, Strength: 100, Arms: 100, Ability: 100}.Write(o))
	must(sangokushi.BattleUnit{Addr: unitA, General: generalA, X: 1, Y: 1, Troops: 10000}.Write(o))
	must(sangokushi.BattleUnit{Addr: unitD, General: generalD, X: 2, Y: 1, Troops: 10000}.Write(o))
	// 被攻擊者站在地形類型 0（倍率 1）上，讓除數以外的項都不動。
	must(sangokushi.SetTerrain(o, 2, 1, 0))

	// 亂數：`sub_65530` 只叫 `rand(500)`（知力那條路不會走到）。
	// 給 400 讓雙方的損失都是負的——只驗攻擊方會漏掉「守方不受除數影響」。
	sangokushi.ForceRand(o, func(n uint32) uint32 {
		if n == 500 {
			return 400
		}
		return 0
	})
	sangokushi.IsolateAttackFormula(o)
}

// TestVolleyDivisor 對照一斉攻撃的除數。
//
// `docs/mechanics/40-military.md`（`sangokushi_x68k_cht`）說：
//
//	被攻擊者 D 的損失 = sub_65530(D, A)                       ← 與 k 無關
//	攻擊者   A 的損失 = sub_65530(A, D) × 地形倍率 ÷ (2k − 1)
//
// 那一條先前是靠 MAME 上的戰場實測定的（`docs/playtest/06` §35／§38／§39），
// 一次驗證要打一場仗。這裡改成**直接問原版的那支函式**：
// 擺兩個合成單位、把亂數固定、對 k = 1..6 各叫一次 `sub_655B6`，
// 看兵數少了多少。整支測試不到一秒，而且不需要磁碟。
func TestVolleyDivisor(t *testing.T) {
	o := loadExe(t)
	setupAttack(t, o)
	base := o.Snapshot()

	// 戰力(A) = 補正×16 + 0 + 0 + 10×2       = 補正×16 + 20
	// 戰力(D) = 補正×16 + 100 + 100 + 100×2  = 補正×16 + 400
	// sub_65530(A, D) = min(0, 20 − 400 − min(400, D.兵)) = −780
	// sub_65530(D, A) = min(0, 400 − 20 − min(400, A.兵)) = −20
	const (
		attackerRaw = 780
		defenderHit = 20
	)
	for k := uint32(1); k <= 6; k++ {
		o.Restore(base)
		if _, err := o.Call(sangokushi.Attack, unitA, unitD, k); err != nil {
			t.Fatalf("k=%d：%v", k, err)
		}
		gotA, err := sangokushi.Troops(o, unitA)
		if err != nil {
			t.Fatal(err)
		}
		gotD, err := sangokushi.Troops(o, unitD)
		if err != nil {
			t.Fatal(err)
		}
		lossA, lossD := 10000-gotA, 10000-gotD
		wantA := attackerRaw / (2*k - 1)
		if lossA != wantA {
			t.Errorf("k=%d：攻擊者損失 %d，公式說 %d ÷ %d = %d",
				k, lossA, attackerRaw, 2*k-1, wantA)
		}
		if lossD != defenderHit {
			t.Errorf("k=%d：被攻擊者損失 %d，應該與 k 無關（%d）", k, lossD, defenderHit)
		}
		t.Logf("k=%d 除數 %d：攻擊者 −%d、被攻擊者 −%d", k, 2*k-1, lossA, lossD)
	}
}

// TestAttackLossFormula 是上一支的**正對照**：直接問 `sub_65530(X, Y)`，
// 把 −780／−20 這兩個數字本身也驗過，而不是拿它們當假設。
//
//	sub_65530(X, Y) = min(0, 戰力(X) − 戰力(Y) − min(rand(500), Y.兵))
func TestAttackLossFormula(t *testing.T) {
	o := loadExe(t)
	setupAttack(t, o)

	// 戰力差先各自問一次：補正對雙方相同，所以差是 20 − 400 = −380。
	pa, err := o.Call(sangokushi.Power, generalA)
	if err != nil {
		t.Fatal(err)
	}
	pd, err := o.Call(sangokushi.Power, generalD)
	if err != nil {
		t.Fatal(err)
	}
	if int32(pd-pa) != 380 {
		t.Fatalf("戰力差是 %d，公式說 400 − 20 = 380（A=%d D=%d）", int32(pd-pa), pa, pd)
	}

	for _, tc := range []struct {
		name string
		x, y uint32
		want int32
	}{
		{"攻擊者自己的損失", unitA, unitD, -780}, // 20 − 400 − 400
		{"被攻擊者的損失", unitD, unitA, -20},   // 400 − 20 − 400
	} {
		got, err := o.Call(sangokushi.AttackLoss, tc.x, tc.y)
		if err != nil {
			t.Fatalf("%s：%v", tc.name, err)
		}
		if int32(got) != tc.want {
			t.Errorf("%s ＝ %d，公式說 %d", tc.name, int32(got), tc.want)
		}
	}
}

// TestVolleyTerrainMultiplier 對照「攻擊者反受損失以被攻擊者所在地形加倍」。
//
// `0x7757A` = `[1, 2, 1, 1, 4, 0]`，索引是 `(cell & 0x3F) >> 3`。
// 站在城（類型 4）上的敵人打起來自己損失 ×4；站在山（類型 5）上則 ×0。
func TestVolleyTerrainMultiplier(t *testing.T) {
	o := loadExe(t)
	setupAttack(t, o)
	base := o.Snapshot()

	for _, tc := range []struct {
		cell byte
		mul  uint32
	}{{0x00, 1}, {0x08, 2}, {0x10, 1}, {0x18, 1}, {0x20, 4}, {0x28, 0}} {
		o.Restore(base)
		if err := sangokushi.SetTerrain(o, 2, 1, tc.cell); err != nil {
			t.Fatal(err)
		}
		if _, err := o.Call(sangokushi.Attack, unitA, unitD, 1); err != nil {
			t.Fatalf("地形 0x%02X：%v", tc.cell, err)
		}
		gotA, err := sangokushi.Troops(o, unitA)
		if err != nil {
			t.Fatal(err)
		}
		if loss, want := 10000-gotA, 780*tc.mul; loss != want {
			t.Errorf("地形 0x%02X（類型 %d）：攻擊者損失 %d，倍率表說 %d",
				tc.cell, tc.cell>>3, loss, want)
		}
	}
}
