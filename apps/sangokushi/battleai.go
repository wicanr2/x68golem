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

	// 名額指派 `sub_680E8(cb_target, cb_act, n)`（AI-8）。
	Assign      = 0x680E8 // 逐槽發名額的本體
	ActingSlots = 0x77C46 // 行動方槽陣列的**指標**（不是陣列本身）
	SlotStride  = 0x24    // 一個槽 36 bytes；`sub_680E8` 固定走 10 個
	Emerg       = 0x674E2 // sub_674E2(U)：緊急處置（非 0 ＝ 這個單位已處理完）
	CounterAtk  = 0x67B68 // sub_67B68(&bx, &by, U)：守方近身反擊；非 0 ＝ 已反擊
	AdvanceTo   = 0x67C76 // sub_67C76(U, bx, by, 0x1F)：朝推進目標走
	ForageChk   = 0x64F42 // 本陣奪糧檢查
	PanelDraw   = 0x634AA // 面板重繪
	BattleOver  = 0x66536 // 戰鬥是否結束

	// 目標選擇（AI-8 的兩個 callback 與它們共用的底層）。
	PickTarget = 0x67968 // sub_67968(&x, &y, mode, U)：本陣或城格
	CastleHQ   = 0x67EFE // sub_67EFE(&x, &y, U) ＝ sub_67968(…, 1, U)
	LordCell   = 0x67EAE // sub_67EAE(&x, &y, U)：對方君主本人／総大将的格
	BestThreat = 0x67F22 // sub_67F22(&x, &y, U)：對方單位裡 sub_6788C 最高的格
	ThreatAt   = 0x6788C // sub_6788C(x, y, ruler)：該格周圍的地形係數和
	MainAct    = 0x67CEC // sub_67CEC(U, x, y)：空格就走過去，有人就交單位層
	CellUnit   = 0x64BF8 // sub_64BF8(x, y, dir)：相鄰格的單位槽指標
	PickEnemy  = 0x666FA // sub_666FA(V, dir)：V 旁邊要打哪一個敵人

	// 整輪對拍要靜音／要讓步的幾支。
	CursorTo    = 0x5BBA8 // sub_5BBA8(x, y)：游標視窗（＝ ActWindow）
	BattleMsg   = 0x5C042 // 戰場訊息列
	BusyWait    = 0x61572 // sub_61572(n)：忙等（動畫節奏）
	SeizeCheck  = 0x64F42 // 本陣奪糧檢查
	OverCheck   = 0x66536 // 戰鬥是否結束
	ActEvade    = 0x676D4 // sub_676D4(U)：走避場
	MoveField   = 0x66E9C // sub_66E9C(U, 場, flags)：沿場下降
	ActRetreat  = 0x66FE6 // sub_66FE6(U, 目的地)：退卻

	// 城格清單（`sub_64718` `0x64af6-0x64bf0` 建，`sub_645FE` 做配對）。
	Pairing     = 0x645FE // sub_645FE(adj, depth)：最少組數的分支界限
	CastleCells = 0x77102 // 20 bytes：城格座標對，`0xFF` 結束
	CastlePairs = 0x77118 // 20 bytes：配對結果

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
	N    int    // 交給 `sub_680E8` 的名額（attrition／collapse 不呼叫它，記 −1）
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

	// 持久與總崩潰不經過 `sub_680E8`，各自有一圈逐槽的迴圈——攔在入口。
	stop := func(addr uint32, kind string) {
		o.Intercept(addr, func(*x68k.Frame) (uint32, bool) {
			*out = PolicyPick{Kind: kind, N: -1}
			return 0, true
		})
	}
	stop(PolAttrition, "attrition")
	stop(PolCollapse, "collapse")

	// 另外三個**讓它跑**，只在入口記下是哪一支，名額由 `sub_680E8` 那邊抓。
	// 名額是 `sub_682CC`／`sub_68278` 自己算出來的（`sub_63196(行動方) ÷ 2`、
	// `(sub_63196(行動方) + 1) ÷ 2`），攔在入口就看不到。
	note := func(addr uint32, kind string) {
		o.OnCall(addr, func(*x68k.Frame) { *out = PolicyPick{Kind: kind, N: -1} })
	}
	note(PolForage, "forage")
	note(PolDecapit, "decapit")
	note(PolMainAtk, "main-atk")
	o.Intercept(Assign, func(f *x68k.Frame) (uint32, bool) {
		if v, err := xc.Long(f, 2); err == nil {
			out.N = int(int32(v))
		}
		return 0, true
	})
}

// AssignStep 是 `sub_680E8` 對一個槽下的決定。
type AssignStep struct {
	Slot int    `json:"slot"` // 第幾個槽（0..9）
	Kind string `json:"kind"` // "main" ＝ cb_act（走 cb_target 的目標）／"advance" ＝ sub_67C76
	N    int    `json:"n"`    // 決定**之後**的剩餘名額
}

// AssignWorld 是名額指派要問外界的每一件事。
//
// `sub_680E8` 本體只做「排名 → 比名額 → 二選一」，其餘全是呼叫別人：
// 緊急處置、守方反擊、兩個目標、面板、結束判定。全部攔掉之後它就是純函式。
type AssignWorld struct {
	BX, BY    int          // sub_67B68 寫回的推進目標
	AX, AY    int          // cb_target 寫回的主目標
	HasA      bool         // cb_target 回非 0（有主目標）
	Emergency map[int]bool // 這些槽的 sub_674E2 回 1
	Counter   map[int]bool // 這些槽的 sub_67B68 回 1（已反擊）
}

// CaptureAssign 把 `sub_680E8` 變成純函式：外界那幾支全部由 w 給答案，
// 兩個 callback 指到 stubTarget／stubAct 這兩個假位址（攔截點是看 PC 的，
// 位址上有沒有程式碼無所謂），落到哪一支就記一步。
//
// slotBase 是十個槽的起點；呼叫端要自己把 `ActingSlots` 指過去。
func CaptureAssign(o *oracle.Oracle, slotBase, stubTarget, stubAct uint32,
	w *AssignWorld, out *[]AssignStep) {
	slotOf := func(f *x68k.Frame, n int) int {
		v, err := xc.Long(f, n)
		if err != nil {
			return -1
		}
		return int(v-slotBase) / SlotStride
	}
	yes := func(m map[int]bool, k int) uint32 {
		if m != nil && m[k] {
			return 1
		}
		return 0
	}
	// 剩餘名額是 `sub_680E8` 的第三個參數（`arg_8`），它自己會用
	// `sub_609FE` 就地改。`link a6` 之後參數在 `8(a6)` 起，而 A6 在
	// callback 裡還是 `sub_680E8` 的框——攔截點是在執行 stub 的第一道
	// 指令**之前**觸發的，什麼都還沒動。
	quota := func(f *x68k.Frame) int {
		v, err := o.Long(f.Machine().CPU.State.A[6] + 16)
		if err != nil {
			return 0
		}
		return int(int32(v))
	}

	// `CapturePolicy` 也在 `Assign` 上裝過攔截點（它要抓名額）。攔截點以位址
	// 為鍵、後裝的蓋掉先裝的，這裡明白地裝一個「不要略過原函式」把它換掉——
	// 不然 `sub_680E8` 的本體根本不會跑，這一段就變成量自己的攔截器。
	o.Intercept(Assign, func(*x68k.Frame) (uint32, bool) { return 0, false })

	o.Intercept(Emerg, func(f *x68k.Frame) (uint32, bool) {
		return yes(w.Emergency, slotOf(f, 0)), true
	})
	o.Intercept(CounterAtk, func(f *x68k.Frame) (uint32, bool) {
		// 不論有沒有反擊，(bx, by) 都會被寫（`sub_67B68` 步驟 1、2）。
		bx, _ := xc.Long(f, 0)
		by, _ := xc.Long(f, 1)
		_ = o.SetLong(bx, uint32(int32(w.BX)))
		_ = o.SetLong(by, uint32(int32(w.BY)))
		return yes(w.Counter, slotOf(f, 2)), true
	})
	o.Intercept(stubTarget, func(f *x68k.Frame) (uint32, bool) {
		ax, _ := xc.Long(f, 0)
		ay, _ := xc.Long(f, 1)
		_ = o.SetLong(ax, uint32(int32(w.AX)))
		_ = o.SetLong(ay, uint32(int32(w.AY)))
		if w.HasA {
			return 1, true
		}
		return 0, true
	})
	o.Intercept(stubAct, func(f *x68k.Frame) (uint32, bool) {
		*out = append(*out, AssignStep{Slot: slotOf(f, 0), Kind: "main", N: quota(f)})
		return 0, true
	})
	o.Intercept(AdvanceTo, func(f *x68k.Frame) (uint32, bool) {
		*out = append(*out, AssignStep{Slot: slotOf(f, 0), Kind: "advance", N: quota(f)})
		return 0, true
	})
	for _, a := range []uint32{ForageChk, PanelDraw, BattleOver} {
		o.Intercept(a, func(*x68k.Frame) (uint32, bool) { return 0, true })
	}
}

// WriteAssignSlots 擺出十個槽：occupied[i] 為假就是空槽（`+0` 記 0）。
func WriteAssignSlots(o *oracle.Oracle, base uint32, xs, ys []int, occupied []bool) error {
	for i := 0; i < 10; i++ {
		a := base + uint32(i)*SlotStride
		for j := uint32(0); j < SlotStride; j++ {
			if err := o.SetByte(a+j, 0); err != nil {
				return err
			}
		}
		if !occupied[i] {
			continue
		}
		// `+0` 只要非 0 就算有單位；`sub_680E8` 本身不解參照它。
		if err := o.SetLong(a+UnitGeneral, 1); err != nil {
			return err
		}
		if err := o.SetLong(a+UnitX, uint32(int32(xs[i]))); err != nil {
			return err
		}
		if err := o.SetLong(a+UnitY, uint32(int32(ys[i]))); err != nil {
			return err
		}
	}
	return o.SetLong(ActingSlots, base)
}

// BuildCastleList 重現 `sub_64718` `0x64962-0x64bf0` 建城格清單那一段。
//
// 為什麼要在這裡重建而不是呼叫 `sub_64718`：那一支同時做整個戰場的畫面
// 初始化（`sub_712BC` 貼圖、`sub_62FEC` 畫框），無頭跑不起來。這裡只搬
// 「掃城格 → 建鄰接表 → `sub_645FE` 配對 → 展開成座標」那四步，
// **配對那一步（分支界限）是呼叫原版自己的 `sub_645FE`**——會算錯的地方在那裡。
//
// adj 是 60 bytes 的暫存（10 格 × 6 方向）。
func BuildCastleList(o *oracle.Oracle, adj, scratch uint32) error {
	// 1. 掃出城格（工作副本的 `cell == 0x20`），y 外 x 內。
	var xs, ys []int
	for y := 0; y < BattleRows; y++ {
		for x := 0; x < TerrainCols; x++ {
			v, err := o.Byte(Terrain + uint32(y*TerrainCols+x))
			if err != nil {
				return err
			}
			// bit7 是「這一格有單位」，跟地形無關——`sub_64718` 是在佈陣之前
			// 跑的，看到的地形沒有那個位元，所以這裡要遮掉。
			if v&0x7F == 0x20 && len(xs) < 10 {
				xs, ys = append(xs, x), append(ys, y)
			}
		}
	}
	// 2. 鄰接表：adj[i*6 + dir-1] = j+1（第 j 個城格在那個方向）。
	for i := 0; i < 10; i++ {
		for k := 0; k < 6; k++ {
			if err := o.SetByte(adj+uint32(i*6+k), 0); err != nil {
				return err
			}
		}
	}
	for i := range xs {
		for dir := 1; dir <= 6; dir++ {
			nx, ny, ok, err := CallNeighbour(o, scratch, xs[i], ys[i], dir)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			for j := range xs {
				if xs[j] == nx && ys[j] == ny {
					if err := o.SetByte(adj+uint32(i*6+dir-1), byte(j+1)); err != nil {
						return err
					}
					break
				}
			}
		}
	}
	// 沒用到的列標 0x80（`sub_6459A` 用 bit7 當「已處理」）。
	for i := len(xs); i < 10; i++ {
		if err := o.SetByte(adj+uint32(i*6), 0x80); err != nil {
			return err
		}
	}
	// 3. 配對：`0x7712C` 是「目前最好的組數」，初值 11。
	for i := uint32(0); i < 20; i++ {
		if err := o.SetByte(CastleCells+i, 0); err != nil {
			return err
		}
		if err := o.SetByte(CastlePairs+i, 0); err != nil {
			return err
		}
	}
	if err := o.SetLong(CastleGroups, 11); err != nil {
		return err
	}
	if _, err := o.Call(Pairing, adj, 0); err != nil {
		return err
	}
	if len(xs) == 0 {
		return o.SetByte(CastleCells, 0xFF)
	}
	// 4. 展開：先把每一組的「另一半」接在 `2×組數` 之後，再把索引換成座標。
	groups, err := o.Long(CastleGroups)
	if err != nil {
		return err
	}
	raw := make([]byte, 20)
	for i := range raw {
		if raw[i], err = o.Byte(CastleCells + uint32(i)); err != nil {
			return err
		}
	}
	seq := make([]byte, 0, 20)
	seq = append(seq, raw[:2*groups]...)
	for k := 0; k < 10; k++ {
		if raw[2*k+1] != 0 {
			seq = append(seq, raw[2*k+1], raw[2*k])
		}
	}
	for k := 0; k < len(xs); k++ {
		i1, i2 := int(seq[2*k]), int(seq[2*k+1])
		if err := o.SetByte(CastleCells+uint32(2*k), byte(xs[i1-1])); err != nil {
			return err
		}
		if err := o.SetByte(CastleCells+uint32(2*k+1), byte(ys[i1-1])); err != nil {
			return err
		}
		px, py := byte(0xFF), byte(0xFF)
		if i2 != 0 {
			px, py = byte(xs[i2-1]), byte(ys[i2-1])
		}
		if err := o.SetByte(CastlePairs+uint32(2*k), px); err != nil {
			return err
		}
		if err := o.SetByte(CastlePairs+uint32(2*k+1), py); err != nil {
			return err
		}
	}
	return o.SetByte(CastleCells+uint32(2*len(xs)), 0xFF)
}

// ReadCastleList 取回 `0x77102`／`0x77118` 的 20 bytes 與組數。
func ReadCastleList(o *oracle.Oracle) (cells, pairs []int, groups int, err error) {
	cells, pairs = make([]int, 20), make([]int, 20)
	for i := 0; i < 20; i++ {
		c, e := o.Byte(CastleCells + uint32(i))
		if e != nil {
			return nil, nil, 0, e
		}
		p, e := o.Byte(CastlePairs + uint32(i))
		if e != nil {
			return nil, nil, 0, e
		}
		cells[i], pairs[i] = int(c), int(p)
	}
	g, e := o.Long(CastleGroups)
	if e != nil {
		return nil, nil, 0, e
	}
	return cells, pairs, int(int32(g)), nil
}

// ── 戰略層（`docs/mechanics/70-ai.md`）──────────────────────────────────

const (
	// NationTable 國記錄表：`(國號 − 1) * NationStride`。
	NationTable  = 0x7A1AA
	NationStride = 0x58
	NatNo        = 0x00 // 國號（`sub_621B0` 比的就是這一個 byte）
	NatOwner     = 0x22 // 君主編號（0 ＝ 空白地）
	NatAdj       = 0x24 // 鄰國串（byte，0 結尾）
	NatWar       = 0x4E // 交戰對象的君主編號（0 ＝ 未交戰）

	CurNation = 0x7B59A // 目前處理中的國記錄指標

	ThreatScore = 0x59848 // sub_59848(N)：威脅分數
	CanAttack   = 0x569C0 // sub_569C0(T)：T 可不可以當戰爭目標（本國取自 CurNation）
	NationAdjTo = 0x621B0 // sub_621B0(T, N)：**T 的鄰國串裡有沒有 N**
	GensInAt    = 0x618A4 // sub_618A4(T, ruler)：ruler 在 T 的武將數
)

// NationRec 是寫進國記錄表的一筆（只寫戰略層讀得到的那幾個欄位）。
type NationRec struct {
	No       byte
	Owner    byte
	War      byte
	Adjacent []byte
}

// Addr 這一筆在表裡的位址。
func (n NationRec) Addr() uint32 {
	return uint32(NationTable) + uint32(n.No-1)*NationStride
}

// Write 先整筆清 0 再寫欄位。
func (n NationRec) Write(o *oracle.Oracle) error {
	a := n.Addr()
	for i := uint32(0); i < NationStride; i++ {
		if err := o.SetByte(a+i, 0); err != nil {
			return err
		}
	}
	for off, v := range map[uint32]byte{NatNo: n.No, NatOwner: n.Owner, NatWar: n.War} {
		if err := o.SetByte(a+off, v); err != nil {
			return err
		}
	}
	for i, t := range n.Adjacent {
		if err := o.SetByte(a+NatAdj+uint32(i), t); err != nil {
			return err
		}
	}
	return o.SetByte(a+NatAdj+uint32(len(n.Adjacent)), 0)
}

const (
	// GeneralTable 武將主表：`0x7BE82`，步長 `0x2C`，**255 筆**（`sub_60E2A`
	// 從第 0 筆掃到第 254 筆）。
	GeneralTable  = 0x7BE82
	GeneralStride = 0x2C
	GeneralCount  = 255
	GenNation     = 0x24 // 所在國號（0 ＝ 不在任何國）

	CountIf    = 0x60E2A // sub_60E2A(N, pred)：整張武將表裡滿足 pred 的筆數
	ActiveGens = 0x60E66 // sub_60E66(N)：該國君主的、在該國的武將數
	GensOfLord = 0x618A4 // sub_618A4(N, ruler)：ruler 的、在 N 的武將數
	NextHop    = 0x5A804 // sub_5A804(N, T)：往 T 的下一站（0 ＝ 到不了）
)

// GenRec 是武將主表的一筆（只寫戰略層讀得到的欄位）。
type GenRec struct {
	Index  int
	Nation byte
	Lord   byte
}

// WriteGeneralTable 整張表先清 0 再寫進去。
func WriteGeneralTable(o *oracle.Oracle, recs []GenRec) error {
	for i := 0; i < GeneralCount; i++ {
		a := uint32(GeneralTable) + uint32(i)*GeneralStride
		for j := uint32(0); j < GeneralStride; j++ {
			if err := o.SetByte(a+j, 0); err != nil {
				return err
			}
		}
	}
	for _, r := range recs {
		a := uint32(GeneralTable) + uint32(r.Index)*GeneralStride
		if err := o.SetByte(a+GenNation, r.Nation); err != nil {
			return err
		}
		if err := o.SetByte(a+GenRuler, r.Lord); err != nil {
			return err
		}
	}
	return nil
}
