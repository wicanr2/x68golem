package sangokushi

import (
	"fmt"

	"github.com/wicanr2/x68golem/internal/x68k"
	"github.com/wicanr2/x68golem/oracle"
)

// 戰場公式的位址與版面。出處一律是 `sangokushi_x68k_cht`，
// 而且**每一個都在那邊標了 L0（逐指令讀出來的）**：
//
//	docs/mechanics/40-military.md  §戰鬥公式、§攻擊種類與 mode 常數
//	docs/formats/05-battle-state.md §單位槽陣列、§全域
const (
	// Attack 是 `sub_655B6(A, D, k)`：算一次攻擊的雙方損失並套用。
	//
	//	被攻擊者 D 的損失 = sub_65530(D, A)
	//	攻擊者   A 的損失 = sub_65530(A, D) × 0x7757A[D 所在地形類型] ÷ (2k − 1)
	//
	// 通常攻撃傳 k = 1；一斉攻撃傳的是**圍著目標的攻擊方單位數**，不是選單序號。
	Attack = 0x655B6

	// AttackLoss 是 `sub_65530(X, Y)`：一方的損失（非正數）。
	AttackLoss = 0x65530

	// Power 是 `sub_654F8(G)`＝ 補正(G)×16 + G[+0x27] + G[+0x28] + G[+0x17]×2。
	Power = 0x654F8

	// DrawUnit（`sub_632B0`）把單位的槽指標寫進單位格表 `0x77996[y×14+x]`
	// 再畫出來；AttackSound（`sub_62D16(n)`）播放音效 n+9，
	// 開關是 `0x74E42`（檔案初始值 1 ＝ 開）。
	//
	// 兩支都與損失公式無關。**但兩支在合成盤面上都跑得起來**（量過的，
	// `docs/findings/023`）——所以攔它們是選擇，不是必要。
	DrawUnit    = 0x632B0
	AttackSound = 0x62D16

	// Terrain 是本場地形的工作副本（168 bytes，列寬 14）。
	// `sub_655B6` 以 `(cell & 0x3F) >> 3` 當類型去查 TerrainLossMul。
	Terrain     = 0x75418
	TerrainCols = 14

	// TerrainLossMul 是 6 個 long：**攻擊者反受損失的倍率**，
	// 以被攻擊者所在地形類型查（`[1, 2, 1, 1, 4, 0]`）。
	TerrainLossMul = 0x7757A

	// Difficulty 是「電腦の強さ」（補正 = 難度 − 1，玩家方為 0）。
	Difficulty = 0x7A144
)

// 戰場單位槽（半單位）的欄位位移（`docs/formats/05`）。
const (
	UnitGeneral  = 0x00 // 武將記錄指標（0 = 空槽）
	UnitX        = 0x04
	UnitY        = 0x08
	UnitTroops   = 0x0C
	UnitMobility = 0x10
	UnitSize     = 0x24
)

// 武將記錄的欄位位移（`docs/formats/03` ＋ `sub_654F8`／`sub_65530` 逐指令）。
const (
	GenIntelligence = 0x16 // 知力：≥ 0x60 才會走「這次不損兵」那條路
	GenStrength     = 0x17 // 武力
	GenRuler        = 0x25 // 所屬君主編號
	GenArms         = 0x27 // 兵士武装
	GenAbility      = 0x28 // 兵士能力
	GenSize         = 0x2C
)

// General 是一筆合成的武將記錄——**擺盤面用，不是解析原版存檔用**。
type General struct {
	Addr         uint32
	Intelligence byte
	Strength     byte
	Arms         byte
	Ability      byte
	Ruler        byte
}

// Write 把記錄寫進記憶體（先整筆清 0，避免帶到別人的殘值）。
func (g General) Write(o *oracle.Oracle) error {
	for i := uint32(0); i < GenSize; i++ {
		if err := o.SetByte(g.Addr+i, 0); err != nil {
			return err
		}
	}
	for off, v := range map[uint32]byte{
		GenIntelligence: g.Intelligence,
		GenStrength:     g.Strength,
		GenArms:         g.Arms,
		GenAbility:      g.Ability,
		GenRuler:        g.Ruler,
	} {
		if err := o.SetByte(g.Addr+off, v); err != nil {
			return err
		}
	}
	return nil
}

// BattleUnit 是一個合成的戰場單位槽。
type BattleUnit struct {
	Addr    uint32
	General uint32 // 武將記錄的位址
	X, Y    uint32
	Troops  uint32
}

// Write 把單位槽寫進記憶體。
func (u BattleUnit) Write(o *oracle.Oracle) error {
	for i := uint32(0); i < UnitSize; i += 4 {
		if err := o.SetLong(u.Addr+i, 0); err != nil {
			return err
		}
	}
	for off, v := range map[uint32]uint32{
		UnitGeneral: u.General,
		UnitX:       u.X,
		UnitY:       u.Y,
		UnitTroops:  u.Troops,
	} {
		if err := o.SetLong(u.Addr+off, v); err != nil {
			return err
		}
	}
	return nil
}

// Troops 讀單位槽現在的兵數。
func Troops(o *oracle.Oracle, unit uint32) (uint32, error) {
	return o.Long(unit + UnitTroops)
}

// SetTerrain 設定一格的地形碼（原始 cell 值，類型 = (cell & 0x3F) >> 3）。
func SetTerrain(o *oracle.Oracle, x, y uint32, cell byte) error {
	if x >= TerrainCols || y >= 12 {
		return fmt.Errorf("sangokushi: 戰場座標 (%d,%d) 超出 14×12", x, y)
	}
	return o.SetByte(Terrain+y*TerrainCols+x, cell)
}

// IsolateAttackFormula 把攻擊流程裡與公式無關的兩支（音效與繪圖）攔掉。
//
// `sub_655B6` 開頭叫 `sub_62D16(4)` 播音效、結尾對雙方各叫一次 `sub_632B0`
// 重繪單位。兩支都不影響損失。
//
// **這是選擇，不是必要**：兩支在合成盤面上都跑得起來，攔與不攔算出來的
// 損失逐項相同（`TestVolleyDivisor` 兩種都跑，`docs/findings/023`）。
// 留著它是因為「只跑要對照的那一段」在別的公式上未必同樣幸運——
// 但**用了它就要記得：被攔掉的那幾支不在結論的涵蓋範圍內**。
func IsolateAttackFormula(o *oracle.Oracle) {
	skip := func(*x68k.Frame) (uint32, bool) { return 0, true }
	o.Intercept(DrawUnit, skip)
	o.Intercept(AttackSound, skip)
}
