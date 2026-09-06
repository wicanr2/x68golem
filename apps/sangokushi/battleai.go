package sangokushi

import (
	"fmt"

	"github.com/wicanr2/x68golem/internal/x68k"
	"github.com/wicanr2/x68golem/oracle"
	"github.com/wicanr2/x68golem/runtime/xc"
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

	// Neighbour 是 `sub_63848(&x, &y, dir)`：把 (x,y) 換成 dir 方向的相鄰格，
	// 回傳非 0 表示在界內。**參數是指標**，答案寫回原處。
	Neighbour = 0x63848
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

// ── 單位層決策 `sub_670DA`（AI-4）────────────────────────────────────────
//
// `sub_670DA(U, T, flags)` **不回傳決定**，它直接把決定執行掉。所以對拍的做法是
// 把五個「執行者」攔下來：攔到哪一支、參數是什麼，就是它決定了什麼。
// 五支都是「呼叫完就 return」，所以攔掉之後 `sub_670DA` 等於一支純決策函式。
const (
	// UnitAct 是 `sub_670DA(U, T, flags)`。
	UnitAct = 0x670DA

	// 五個執行者（`docs/mechanics/40` AI-4）。
	ActStandby  = 0x66DC6 // sub_66DC6(U)：待機
	ActRally    = 0x64E7A // sub_64E7A(U)：集結（散開的分隊收回來）
	ActFlank    = 0x64EC2 // sub_64EC2(U, dir)：散開去側翼
	ActApproach = 0x66FA8 // sub_66FA8(U, tx, ty, flags)：走過去
	ActStrike   = 0x65B5A // loc_65B5A(A, D, mode, x, y)：出手

	// ActWindow 是出手前印字的視窗（`sub_5BBA8(0x43, 0x14)`）。與決策無關，攔掉。
	ActWindow = 0x5BBA8

	// 決策讀到的全域。
	Wind        = 0x7760E // 風向 0..5（`docs/formats/05`）
	OwnSupply   = 0x770EA // 行動方的補給分數
	EnemySupply = 0x770EE // 對方的補給分數
	PolicyFlag  = 0x770FE // 火計捷徑的閘（AI-4 `0x67286`）
)

// Act 是 `sub_670DA` 這一次決定做了什麼。
type Act struct {
	Kind string // "standby"／"rally"／"flank"／"approach"／"strike"／"none"
	Mode int    // strike：攻擊種類 1..5
	X, Y int    // approach：目的地；strike：目標座標；flank：方向碼放在 X
}

// CaptureAct 把五個執行者攔下來，決定寫進 out。
//
// **攔掉是選擇，而且這裡是必要的**：五支都會動畫面、動狀態，
// 而要對的是「它決定做什麼」，不是「它怎麼做」。
// 用了它就要記得：被攔掉的那幾支不在結論的涵蓋範圍內。
func CaptureAct(o *oracle.Oracle, out *Act) {
	arg := func(f *x68k.Frame, n int) int {
		v, err := xc.Long(f, n)
		if err != nil {
			return 0
		}
		return int(int32(v))
	}
	set := func(addr uint32, fn func(f *x68k.Frame)) {
		o.Intercept(addr, func(f *x68k.Frame) (uint32, bool) {
			fn(f)
			return 0, true
		})
	}
	set(ActStandby, func(*x68k.Frame) { *out = Act{Kind: "standby"} })
	set(ActRally, func(*x68k.Frame) { *out = Act{Kind: "rally"} })
	set(ActFlank, func(f *x68k.Frame) { *out = Act{Kind: "flank", X: arg(f, 1)} })
	set(ActApproach, func(f *x68k.Frame) {
		*out = Act{Kind: "approach", X: arg(f, 1), Y: arg(f, 2)}
	})
	set(ActStrike, func(f *x68k.Frame) {
		*out = Act{Kind: "strike", Mode: arg(f, 2), X: arg(f, 3), Y: arg(f, 4)}
	})
	set(ActWindow, func(*x68k.Frame) {})
}

// CallNeighbour 呼叫 `sub_63848(&x, &y, dir)`，回傳相鄰格與在不在界內。
// scratch 是兩個 long 的暫存位址（呼叫端自己找一塊空的）。
func CallNeighbour(o *oracle.Oracle, scratch uint32, x, y, dir int) (int, int, bool, error) {
	if err := o.SetLong(scratch, uint32(int32(x))); err != nil {
		return 0, 0, false, err
	}
	if err := o.SetLong(scratch+4, uint32(int32(y))); err != nil {
		return 0, 0, false, err
	}
	r, err := o.Call(Neighbour, scratch, scratch+4, uint32(int32(dir)))
	if err != nil {
		return 0, 0, false, err
	}
	nx, err := o.Long(scratch)
	if err != nil {
		return 0, 0, false, err
	}
	ny, err := o.Long(scratch + 4)
	if err != nil {
		return 0, 0, false, err
	}
	return int(int32(nx)), int(int32(ny)), r != 0, nil
}

// ── 方針層 `sub_68382`（AI-9）───────────────────────────────────────────
//
// `sub_68382` **不吃參數**，四個量與五種方針全部由全域決定。所以對拍的做法是
// 把「讀世界」的五支攔掉（由呼叫端給值）、把五個方針分支攔掉（記錄哪一支被叫），
// 剩下的就是純判定。
const (
	// PolicyTurn 是 `sub_68382()`。
	PolicyTurn = 0x68382

	// 五個方針分支。
	PolAttrition = 0x67F7C // 持久（消耗）
	PolCollapse  = 0x68018 // 總崩潰／全軍退却
	PolForage    = 0x682B0 // sub_682B0(n)：守方奪糧／主力（守）
	PolDecapit   = 0x68278 // 斬首
	PolMainAtk   = 0x682CC // 主力（攻）

	// 火計相關。
	Burnability  = 0x6533A // sub_6533A(x, y)：一格的可燃度
	SpreadChance = 0x6542A // sub_6542A(x, y)：一格本回合的起火機率

	// 讀世界的三支（`SupplyScore`／`TotalTroops` 在上面）。
	Deployable = 0x63196 // sub_63196(ruler)：可上場人數
	NationGens = 0x631EC // sub_631EC(ruler)：該君主在戰場所在國的武將數
	RetreatTo  = 0x622CC // sub_622CC(N, ruler)：退卻目的地（0 ＝ 沒有）

	// 方針層讀到的全域。
	DefRuler     = 0x7761A // 守方君主
	AtkRuler     = 0x77616 // 攻方君主
	OppRuler     = 0x77C42 // 對方君主
	BattleDay    = 0x77612 // 第幾日
	CastleGroups = 0x7712C // 守住全部城格所需的最少單位數
	NationPtr    = 0x7B59E // 戰場所在國的國記錄指標
	OppSlots     = 0x77C4A // 對方的槽陣列
	HQX          = 0x77C36 // 攻方本陣格 x
	HQY          = 0x77C3A // 攻方本陣格 y

	// 君主表：`sub_6153A(r)` 讀 `word_7B5A2 + (r-1)*0x8E + 1` 的 bit0
	// ＝「這個君主是人類玩家」。`sub_6533A`（可燃度）用它決定難度補正的正負號。
	RulerTable  = 0x7B5A2
	RulerStride = 0x8E
	RulerFlags  = 1
)

// SetRulerHuman 設定君主表的「人類玩家」旗標（`sub_6153A` 讀的那個 bit0）。
//
// 不設的話讀到的是 `.Z` 映像裡的殘值——它是固定的，所以對拍不會抖，
// 但 remake 這邊無從得知，火計那條分支就會系統性地對不上。
func SetRulerHuman(o *oracle.Oracle, ruler byte, human bool) error {
	addr := uint32(RulerTable) + uint32(ruler-1)*RulerStride + RulerFlags
	v, err := o.Byte(addr)
	if err != nil {
		return err
	}
	if human {
		v |= 1
	} else {
		v &^= 1
	}
	return o.SetByte(addr, v)
}

// PolicyPick 是 `sub_68382` 這一次選了哪一個方針。
type PolicyPick struct {
	Kind string // "attrition"／"collapse"／"forage"／"decapit"／"main-atk"／"none"
	N    int    // forage：`sub_682B0(n)` 的 n
}

// World 是「讀世界」那幾支要回的值，依君主編號分。
type World struct {
	Supply      map[byte]uint32 // sub_68302(金, 米, ruler)
	Deployable  map[byte]uint32 // sub_63196(ruler)
	NationGens  map[byte]uint32 // sub_631EC(ruler)
	TotalTroops map[byte]uint32 // sub_6342A(ruler)
	Retreat     uint32          // sub_622CC(N, ruler)
}

// CapturePolicy 攔掉讀世界的五支與五個方針分支。
//
// **攔掉讀世界那幾支是必要的**：它們會走 255 筆武將主表與十個槽，
// 要在合成盤面上讓它們算出指定的值，得先擺出一整份遊戲狀態——
// 那是另一個量級的工作，而且擺錯了不會有錯誤訊息。
// 用了它就要記得：被攔掉的那幾支不在結論的涵蓋範圍內。
func CapturePolicy(o *oracle.Oracle, w *World, out *PolicyPick) {
	argByte := func(f *x68k.Frame, n int) byte {
		v, err := xc.Long(f, n)
		if err != nil {
			return 0
		}
		return byte(v)
	}
	pick := func(m map[byte]uint32, k byte) uint32 {
		if m == nil {
			return 0
		}
		return m[k]
	}
	o.Intercept(SupplyScore, func(f *x68k.Frame) (uint32, bool) {
		return pick(w.Supply, argByte(f, 2)), true
	})
	o.Intercept(Deployable, func(f *x68k.Frame) (uint32, bool) {
		return pick(w.Deployable, argByte(f, 0)), true
	})
	o.Intercept(NationGens, func(f *x68k.Frame) (uint32, bool) {
		return pick(w.NationGens, argByte(f, 0)), true
	})
	o.Intercept(TotalTroops, func(f *x68k.Frame) (uint32, bool) {
		return pick(w.TotalTroops, argByte(f, 0)), true
	})
	o.Intercept(RetreatTo, func(*x68k.Frame) (uint32, bool) { return w.Retreat, true })

	branch := func(addr uint32, kind string, withN bool) {
		o.Intercept(addr, func(f *x68k.Frame) (uint32, bool) {
			p := PolicyPick{Kind: kind}
			if withN {
				if v, err := xc.Long(f, 0); err == nil {
					p.N = int(int32(v))
				}
			}
			*out = p
			return 0, true
		})
	}
	branch(PolAttrition, "attrition", false)
	branch(PolCollapse, "collapse", false)
	branch(PolForage, "forage", true)
	branch(PolDecapit, "decapit", false)
	branch(PolMainAtk, "main-atk", false)
}
