package sangokushi

import (
	"fmt"

	"github.com/wicanr2/x68golem/internal/x68k"
	"github.com/wicanr2/x68golem/oracle"
)

// 戰場 AI 的位址與版面。出處一律是 `sangokushi_x68k_cht` 的
// `docs/mechanics/40-military.md` §戰場 AI（第五輪，逐指令 L0）AI-0…AI-11，
// 每一支在那邊都標了逐指令讀出來的位址範圍。
//
// 這一批挑的是**純函式**：同樣的輸入永遠得到同樣的輸出，沒有畫面、沒有音效、
// 不動全域狀態。純函式才對得起「對拍」兩個字——輸出不一樣就是實作不一樣，
// 不會是「跑到不同的時間點」。
const (
	// HexDistance 是 `sub_6661C(x1, y1, x2, y2)`：六角距離
	// `dx + max(0, dr − dx)/2`。
	HexDistance = 0x6661C

	// DirCode 是 `sub_666A8(U, x, y)`：從 U 看 (x,y) 的方向碼 1..6。
	DirCode = 0x666A8

	// ChargeOdds 是 `sub_66E6C(A, D)`：突撃勝算——機動力 ≥ 2、兵多、武力高，
	// 三者全滿足回 1。
	ChargeOdds = 0x66E6C

	// TrickRate 是 `sub_65930(A, D)`：計略成功率。
	// FireRate 是 `sub_659DE(A, D)`：火計成功率。
	TrickRate = 0x65930
	FireRate  = 0x659DE

	// SupplyScore 是 `sub_68302(gold, rice, ruler)`：補給分數。
	//
	// ⚠ **第三個參數是君主編號，不是兵數**（`dump_068302` 逐指令：
	// `068320` 把它推進去叫 `sub_6342A`）。當成兵數傳的話 `sub_6342A`
	// 在垃圾上跑、回 0，兩個 min 都被跳過，函式**一律回 100**——
	// 看起來像「補給總是充足」，不像參數錯了。
	SupplyScore = 0x68302

	// TotalTroops 是 `sub_6342A(ruler)`：該方場上總兵數。
	TotalTroops = 0x6342A

	// DistField 是 `sub_667B0(field, tx, ty, U, avoid_fire)`：距離場。
	// `field` 是呼叫端的 12 列 × 14 欄 long 陣列（列距 56 bytes）。
	DistField = 0x667B0

	// Passable 是 `sub_662E2(U, dir, mob)`：往 dir 走得通嗎。
	Passable = 0x662E2
)

// 戰場的全域版面（`docs/formats/05-battle-state.md`）。
const (
	// UnitGrid 是單位格表：每格 4 bytes 的槽指標，每列 14 格，共 12 列。
	UnitGrid = 0x77996

	// FireGrid 是每格的火焰計數（168 bytes）。
	FireGrid = 0x7761E

	// ActingRuler 是「現在輪到誰行動」的君主編號——距離場的放寬階段 1
	// 用它分辨敵我（`G[+0x25] != 0x77C3E` 才算敵方）。
	ActingRuler = 0x77C3E

	// StepCost 是 AI 距離場的步進成本表（5 個 long）。`sub_667B0` 會**就地
	// 改寫**第 3 項（水域）成 6 或 10，所以每次呼叫前要重設。
	StepCost = 0x770B2

	// BattleRows 是戰場列數；欄數是 TerrainCols。
	BattleRows = 12
)

// GenNavy 是武將記錄的水軍旗標位移（`G[+0x1B]` bit0）。
const GenNavy = 0x1B

// UnitMobilityIsByte 記一件容易寫錯的事：機動力在槽裡是 `+0x10` 的**一個
// byte**，不是 long（`dump_066e6c` 的 `cmpi.b #2,$10(a1)`）。
// 用 long 寫 20 進去，大端序讓 `+0x10` 那個 byte 變成 0，
// 於是「機動力 ≥ 2」永遠不成立——`sub_66E6C` 一律回 0，
// 看起來像「AI 從來不突撃」，不像欄位寬度寫錯。
const UnitMobilityIsByte = true

// SetMobility 設單位槽的機動力（byte）。
func SetMobility(o *oracle.Oracle, unit uint32, v byte) error {
	return o.SetByte(unit+UnitMobility, v)
}

// ForceTotalTroops 把 `sub_6342A(ruler)` 整支換掉，一律回 n。
//
// 給補給分數對拍用：那條公式要的是「兵數」這個量，而原版是從場上的槽
// 加總出來的。**攔掉是選擇**——它讓對拍只涵蓋 `sub_68302` 的算術，
// 不涵蓋加總；加總本身要另外對。
func ForceTotalTroops(o *oracle.Oracle, n uint32) {
	o.Intercept(TotalTroops, func(*x68k.Frame) (uint32, bool) { return n, true })
}

// Board 是一個合成的戰場盤面——**擺給 AI 看用的，不是原版存檔的解析結果**。
//
// 三張表要一起擺：地形（`0x75418`）、火焰（`0x7761E`）、單位格（`0x77996`）。
// **少擺任何一張，距離場都會安靜地算出另一個答案**——它們不是獨立的輸入，
// 是同一個盤面的三個面向。
type Board struct {
	Terrain [BattleRows * TerrainCols]byte   // 工作副本的 cell 值（類型 = (cell&0x3F)>>3）
	Fire    [BattleRows * TerrainCols]byte   // 每格火焰計數
	Occupy  [BattleRows * TerrainCols]uint32 // 每格的單位槽指標（0 = 空）
	Ruler   byte                             // 行動方君主編號
}

// Write 把盤面寫進記憶體。
func (b Board) Write(o *oracle.Oracle) error {
	for i := 0; i < BattleRows*TerrainCols; i++ {
		if err := o.SetByte(Terrain+uint32(i), b.Terrain[i]); err != nil {
			return err
		}
		if err := o.SetByte(FireGrid+uint32(i), b.Fire[i]); err != nil {
			return err
		}
		if err := o.SetLong(UnitGrid+uint32(i)*4, b.Occupy[i]); err != nil {
			return err
		}
	}
	return o.SetByte(ActingRuler, b.Ruler)
}

// ResetStepCost 把 `0x770B2` 的 5 個 long 設回檔案裡的初值 [2,4,5,6,3]。
//
// **一定要在每次呼叫距離場之前做**：`sub_667B0` 開頭會把第 3 項改成
// 6（水軍）或 10（非水軍），改完不會還原。上一次留下的值會讓下一次
// 算出「看起來很合理但是錯的」距離場。
func ResetStepCost(o *oracle.Oracle) error {
	for i, v := range [5]uint32{2, 4, 5, 6, 3} {
		if err := o.SetLong(StepCost+uint32(i)*4, v); err != nil {
			return err
		}
	}
	return nil
}

// CallDistField 呼叫 `sub_667B0` 並讀回 12×14 的距離場。
// fieldAddr 是給原版當輸出緩衝的位址（672 bytes，呼叫端自己找一塊空的）。
func CallDistField(o *oracle.Oracle, fieldAddr, unit uint32, tx, ty int, avoidFire bool) ([]int32, error) {
	if err := ResetStepCost(o); err != nil {
		return nil, err
	}
	var af uint32
	if avoidFire {
		af = 1
	}
	if _, err := o.Call(DistField, fieldAddr, uint32(tx), uint32(ty), unit, af); err != nil {
		return nil, err
	}
	out := make([]int32, BattleRows*TerrainCols)
	for i := range out {
		v, err := o.Long(fieldAddr + uint32(i)*4)
		if err != nil {
			return nil, err
		}
		out[i] = int32(v)
	}
	return out, nil
}

// SetFire 設一格的火焰計數。
func SetFire(o *oracle.Oracle, x, y uint32, n byte) error {
	if x >= TerrainCols || y >= BattleRows {
		return fmt.Errorf("sangokushi: 戰場座標 (%d,%d) 超出 %d×%d", x, y, TerrainCols, BattleRows)
	}
	return o.SetByte(FireGrid+y*TerrainCols+x, n)
}
