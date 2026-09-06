// Command battleai-fixture 問原版的戰場 AI 一批問題，把答案寫成 JSON。
//
// 這是「戰場 AI 對拍」的原版那一半：remake（`sangokushi_x68k_cht`）那邊有一份
// 讀同一個 JSON 的測試，拿自己的實作跑同一組輸入，逐項比對。
//
// 為什麼要走檔案而不是讓 remake 直接呼叫這裡：**remake 不該在建置時需要
// 原版執行檔**。fixture 是純數字，進得了版控；`SANMAIN.Z` 進不了。
//
// 挑的都是**純函式**——同樣的輸入永遠得到同樣的輸出，沒有畫面、沒有音效、
// 不動全域狀態。輸出不一樣就是實作不一樣，不會是「跑到不同的時間點」。
//
// ⚠ **本專案不含任何原版檔案**，`-z` 由玩家自備。
//
//	go run ./cmd/battleai-fixture -z path/to/SANMAIN.Z -o battleai.json
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"

	"github.com/wicanr2/x68golem/apps/sangokushi"
	"github.com/wicanr2/x68golem/internal/x68k"
	"github.com/wicanr2/x68golem/oracle"
	"github.com/wicanr2/x68golem/runtime/xc"
)

// 合成盤面的位址：主記憶體 2 MB，遊戲的堆積到 0xFCA86 為止，樁在 0x1F0000 起。
// 0x1E0000 這一帶誰都不會碰。
const (
	scratch  = 0x1E0000
	fieldBuf = scratch + 0x0000 // 672 bytes
	unitBase = scratch + 0x0400 // 每個單位槽 0x40
	genBase  = scratch + 0x0C00 // 每筆武將記錄 0x40
	slotBase = scratch + 0x1400 // 名額指派用的十個槽（步長 0x24）
	// 兩個假的 callback 位址：`sub_680E8` 的 `jsr (a3)`／`jsr (a4)` 跳過來，
	// 攔截點看的是 PC，位址上有沒有程式碼無所謂。
	stubTarget = scratch + 0x1800
	stubAct    = scratch + 0x1808
	adjBuf     = scratch + 0x1810 // 城格鄰接表 60 bytes
	nationBuf  = scratch + 0x2400 // 戰場所在國的國記錄
	defSlots   = scratch + 0x1900 // 守方十個槽（步長 0x24）
	atkSlots   = scratch + 0x1B00 // 攻方十個槽
	defGens    = scratch + 0x1D00 // 對應的武將記錄（步長 0x30）
	atkGens    = scratch + 0x1F00
	cells      = sangokushi.BattleRows * sangokushi.TerrainCols
)

// 為了讓 JSON 好讀，每一類案例各有自己的結構。
type supplyCase struct {
	Gold, Rice, Soldiers int
	Score                int
}

type chargeCase struct {
	AMob, ATroops, AStr int
	DTroops, DStr       int
	OK                  int
}

type dirCase struct {
	UX, UY, X, Y int
	Dir          int
}

// fieldCase 是一整個盤面。`Occupy` 是每格的佔用狀態：
// 0 空、1 行動方自己的單位、2 敵方單位。
type fieldCase struct {
	Terrain   []int `json:"terrain"`
	Fire      []int `json:"fire"`
	Occupy    []int `json:"occupy"`
	Navy      bool  `json:"navy"`
	UX        int   `json:"ux"`
	UY        int   `json:"uy"`
	TX        int   `json:"tx"`
	TY        int   `json:"ty"`
	AvoidFire bool  `json:"avoidFire"`
	Field     []int `json:"field"`
}

// unitSpec 是對拍時擺出來的一個單位。
type unitSpec struct {
	X, Y     int
	Troops   int
	Mobility int
	Force    int
	Intel    int
	Exp      int
	Split    bool // 已散開（+0x12 非 0）
}

// actCase 是 `sub_670DA(U, T, flags)` 的一次決策。
type actCase struct {
	Terrain string `json:"terrain"` // 168 個 0-9 字元，一格一個
	Fire    string `json:"fire"`
	Wind    int   `json:"wind"`
	OwnSup  int   `json:"ownSupply"`
	EnySup  int   `json:"enemySupply"`
	Flags   int   `json:"flags"`
	UHuman  bool  `json:"uHuman"` // U 的君主是人類玩家（sub_6153A）
	THuman  bool  `json:"tHuman"`
	U       unitSpec
	T       unitSpec
	Kind    string `json:"kind"`
	Mode    int    `json:"mode"`
	DX      int    `json:"dx"`
	DY      int    `json:"dy"`
}

// rateCase 是計略／火計成功率的一次查詢。
type rateCase struct {
	AIntel, AExp, DIntel, DExp int
	Trick, Fire                int
}

// policyCase 是方針層 `sub_68382` 的一次判定。
type policyCase struct {
	Acting     string `json:"acting"` // "defender" 或 "attacker"
	Day        int    `json:"day"`
	Groups     int    `json:"groups"` // 0x7712C 城格分組數
	OwnSup     int    `json:"ownSupply"`
	EnySup     int    `json:"enemySupply"`
	OwnDeploy  int    `json:"ownDeploy"`
	EnyDeploy  int    `json:"enemyDeploy"`
	OwnGens    int    `json:"ownGens"`
	EnyGens    int    `json:"enemyGens"`
	OwnTroops  int    `json:"ownTroops"`
	EnyTroops  int    `json:"enemyTroops"`
	HasRetreat bool   `json:"hasRetreat"`
	Kind       string `json:"kind"`
	N          int    `json:"n"`
}

// burnCase 是可燃度 `sub_6533A` 與起火機率 `sub_6542A` 的一次查詢。
//
// 兩支一起問：`sub_6542A` 在迴圈裡逐次呼叫 `sub_6533A`，分開驗的話
// 「係數乘在每一次的可燃度上」和「乘在累積完的機率上」分不出來。
type burnCase struct {
	Terrain string `json:"terrain"`
	Fire    string `json:"fire"`
	Wind    int   `json:"wind"`
	X       int   `json:"x"`
	Y       int   `json:"y"`
	CPU     int   `json:"cpu"`      // 電腦の強さ（dword_7A144）
	HasUnit bool  `json:"hasUnit"`  // (x, y) 上有沒有單位
	Intel   int   `json:"intel"`    // 該單位武將的知力
	Exp     int   `json:"exp"`      // 経験
	Human   bool  `json:"human"`    // 該武將的君主是人類玩家（sub_6153A）
	Burn    int   `json:"burn"`     // sub_6533A(x, y)
	Spread  int   `json:"spread"`   // sub_6542A(x, y)
}

// assignCase 是名額指派 `sub_680E8(cb_target, cb_act, n)` 的一次判定。
type assignCase struct {
	X       []int                  `json:"x"`      // 十個槽的座標（空槽的值無意義）
	Y       []int                  `json:"y"`
	Filled  []bool                 `json:"filled"` // 哪些槽有單位
	N       int                    `json:"n"`      // 起始名額
	AX      int                    `json:"ax"`     // cb_target 的目標
	AY      int                    `json:"ay"`
	HasA    bool                   `json:"hasA"`   // cb_target 有沒有目標
	BX      int                    `json:"bx"`     // sub_67B68 的推進目標
	BY      int                    `json:"by"`
	Steps   []sangokushi.AssignStep `json:"steps"`
}

// targetCase 是「這個局面下，AI 的兩個目標各是哪一格」。
//
// 涵蓋 `sub_67968`（兩種 mode）、`sub_67EAE`、`sub_67F22`、`sub_6788C`，
// 以及它們共用的城格清單 `0x77102`／`0x77118`／`0x7712C`。
type targetCase struct {
	Terrain string `json:"terrain"` // 168 個 0-9 字元：地形類型（4 ＝ 城格）
	Fire    string `json:"fire"`
	Units   string `json:"units"` // 一格一個字元：'.' 空、'a'..'j' 守方槽、'A'..'J' 攻方槽
	HQX     int    `json:"hqx"`
	HQY     int    `json:"hqy"`
	Acting  string `json:"acting"` // "defender"／"attacker"
	Lords   []int  `json:"lords"`  // 是君主本人（+0x1B bit1）的槽，0..9 守方、10..19 攻方
	Chiefs  []int  `json:"chiefs"` // 是総大将（+0x2A bit7）的槽
	Acted   []int  `json:"acted"`  // 機動力 0xFF（已行動）的槽
	UX      int    `json:"ux"`     // 行動單位所在格
	UY      int    `json:"uy"`
	Navy    bool   `json:"navy"`

	Groups int   `json:"groups"` // 0x7712C
	Cells  []int `json:"cells"`  // 0x77102 的 20 bytes
	Pairs  []int `json:"pairs"`  // 0x77118 的 20 bytes

	Threat []int `json:"threat"` // sub_6788C(x, y, 守方君主) 逐格（168 項）

	T0 [3]int `json:"t0"` // sub_67968(…, 0, U) → x, y, 有沒有找到
	T1 [3]int `json:"t1"` // sub_67968(…, 1, U)
	LC [3]int `json:"lc"` // sub_67EAE
	BT [3]int `json:"bt"` // sub_67F22
}

// advCase 是「拿到目標之後怎麼動」：`sub_666FA`（挑相鄰的敵人）、
// `sub_67C76`（推進目標的執行者）、`sub_67CEC`（主目標的執行者）。
type advCase struct {
	Terrain string `json:"terrain"`
	Fire    string `json:"fire"`
	Units   string `json:"units"`
	Wind    int    `json:"wind"`
	Chiefs  []int  `json:"chiefs"`
	Acted   []int  `json:"acted"`
	Acting  string `json:"acting"`
	UX      int    `json:"ux"`
	UY      int    `json:"uy"`
	Flags   int    `json:"flags"`
	BX      int    `json:"bx"` // 推進目標
	BY      int    `json:"by"`

	Enemy int `json:"enemy"` // sub_666FA(U, 0) 選到的槽（−1 ＝ 沒有）

	// sub_67C76(U, bx, by, flags)
	C76Kind  string `json:"c76Kind"` // "move"／"act"
	C76X     int    `json:"c76x"`    // move：目標格
	C76Y     int    `json:"c76y"`
	C76Enemy int    `json:"c76Enemy"` // act：對象槽
	C76Flags int    `json:"c76Flags"`

	// sub_67CEC(U, bx, by)
	CECKind  string `json:"cecKind"`
	CECEnemy int    `json:"cecEnemy"`
	CECFlags int    `json:"cecFlags"`
}

// strikeRec 是整輪裡的一次出手。
type strikeRec struct {
	U    int `json:"u"`
	T    int `json:"t"`
	Mode int `json:"mode"`
}

// roundCase 是**一整輪**：`sub_68382` 從方針選到每個單位動完。
type roundCase struct {
	Terrain string `json:"terrain"`
	Fire    string `json:"fire"`
	Units   string `json:"units"`
	Troops  []int  `json:"troops"` // 20 個槽
	Mob     []int  `json:"mob"`
	Intel   []int  `json:"intel"`
	Force   []int  `json:"force"`
	Lords   []int  `json:"lords"`
	Chiefs  []int  `json:"chiefs"`
	Wind    int    `json:"wind"`
	Day     int    `json:"day"`
	HQX     int    `json:"hqx"`
	HQY     int    `json:"hqy"`
	Acting  string `json:"acting"`
	Gold    int    `json:"gold"`
	Rice    int    `json:"rice"`
	AtkGold int    `json:"atkGold"`
	AtkRice int    `json:"atkRice"`

	OwnDeploy int `json:"ownDeploy"`
	EnyDeploy int `json:"enyDeploy"`
	OwnGens   int `json:"ownGens"`
	EnyGens   int `json:"enyGens"`
	Groups    int `json:"groups"`

	Policy  string      `json:"policy"`
	Quota   int         `json:"quota"`
	Strikes []strikeRec `json:"strikes"`
	FinalX  []int       `json:"finalX"`
	FinalY  []int       `json:"finalY"`
	FinalM  []int       `json:"finalM"`
}

type fixture struct {
	Note        string        `json:"note"`
	ExeSHA256   string        `json:"exeSha256"`
	Cols        int           `json:"cols"`
	Rows        int           `json:"rows"`
	HexDistance [][5]int      `json:"hexDistance"` // x1,y1,x2,y2,d
	DirCode     []dirCase     `json:"dirCode"`
	Supply      []supplyCase  `json:"supply"`
	ChargeOdds  []chargeCase  `json:"chargeOdds"`
	DistField   []fieldCase   `json:"distField"`
	UnitAct     []actCase     `json:"unitAct"`
	Rates       []rateCase    `json:"rates"`
	Neighbour   [][6]int      `json:"neighbour"` // x,y,dir,nx,ny,inBounds
	Policy      []policyCase  `json:"policy"`
	Burn        []burnCase    `json:"burn"`
	Assign      []assignCase  `json:"assign"`
	Targets     []targetCase  `json:"targets"`
	Advance     []advCase     `json:"advance"`
	Round       []roundCase   `json:"round"`
}

func main() {
	z := flag.String("z", "", "SANMAIN.Z（必填，玩家自備）")
	out := flag.String("o", "battleai.json", "輸出的 fixture")
	boards := flag.Int("boards", 24, "隨機盤面數")
	seed := flag.Int64("seed", 1, "亂數種子（固定才能重現）")
	acts := flag.Int("acts", 400, "單位層決策的案例數")
	policies := flag.Int("policies", 400, "方針層的案例數")
	burns := flag.Int("burns", 400, "可燃度／起火機率的案例數")
	assigns := flag.Int("assigns", 400, "名額指派的案例數")
	tgts := flag.Int("targets", 200, "目標選擇的案例數")
	advs := flag.Int("advance", 400, "推進執行者的案例數")
	rounds := flag.Int("rounds", 200, "整輪的案例數")
	flag.Parse()
	if *z == "" {
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(*z)
	die(err)
	sum := sha256.Sum256(raw)

	o, err := oracle.Load(oracle.Config{Exe: *z, LatchIO: true})
	die(err)

	f := fixture{
		Note: "原版 SANMAIN.Z 的戰場 AI 純函式回答；由 x68golem 的 " +
			"cmd/battleai-fixture 產生，不要手改。",
		ExeSHA256: hex.EncodeToString(sum[:]),
		Cols:      sangokushi.TerrainCols,
		Rows:      sangokushi.BattleRows,
	}

	// ── 六角距離 sub_6661C ────────────────────────────────────────────
	rng := rand.New(rand.NewSource(*seed))
	for i := 0; i < 200; i++ {
		x1, y1 := rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
		x2, y2 := rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
		d, err := o.Call(sangokushi.HexDistance, u32(x1), u32(y1), u32(x2), u32(y2))
		die(err)
		f.HexDistance = append(f.HexDistance, [5]int{x1, y1, x2, y2, int(int32(d))})
	}

	// ── 方向碼 sub_666A8(U, x, y) ─────────────────────────────────────
	for i := 0; i < 120; i++ {
		ux, uy := rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
		x, y := rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
		die(sangokushi.BattleUnit{Addr: unitBase, General: genBase,
			X: u32(ux), Y: u32(uy), Troops: 1000}.Write(o))
		d, err := o.Call(sangokushi.DirCode, unitBase, u32(x), u32(y))
		die(err)
		f.DirCode = append(f.DirCode, dirCase{ux, uy, x, y, int(int32(d))})
	}

	// ── 補給分數 sub_68302 ────────────────────────────────────────────
	// **範圍要蓋到公式真的在算的那一段**：金米給大值時分數一律封頂 100，
	// 全部取大值等於只驗了那個 min。所以一半取小值。
	for i := 0; i < 200; i++ {
		g, r, s := rng.Intn(30001), rng.Intn(30001), rng.Intn(60000)+1
		if i%2 == 0 {
			g, r = rng.Intn(4000), rng.Intn(4000)
		}
		// 第三個參數是君主編號；兵數由 `sub_6342A` 加總出來，這裡攔掉直接給。
		sangokushi.ForceTotalTroops(o, u32(s))
		v, err := o.Call(sangokushi.SupplyScore, u32(g), u32(r), 0)
		die(err)
		f.Supply = append(f.Supply, supplyCase{g, r, s, int(int32(v))})
	}

	// ── 突撃勝算 sub_66E6C ────────────────────────────────────────────
	// 同理：隨機取值時三個條件同時成立的機率很低，一半的案例刻意讓 A 佔上風。
	for i := 0; i < 200; i++ {
		c := chargeCase{
			AMob: rng.Intn(30), ATroops: rng.Intn(20000), AStr: rng.Intn(120),
			DTroops: rng.Intn(20000), DStr: rng.Intn(120),
		}
		if i%2 == 0 {
			c.AMob = 2 + rng.Intn(20)
			c.DTroops = rng.Intn(8000)
			c.ATroops = c.DTroops + rng.Intn(4000)
			c.DStr = rng.Intn(60)
			c.AStr = c.DStr + rng.Intn(30)
		}
		die(sangokushi.General{Addr: genBase, Strength: byte(c.AStr)}.Write(o))
		die(sangokushi.General{Addr: genBase + 0x40, Strength: byte(c.DStr)}.Write(o))
		die(sangokushi.BattleUnit{Addr: unitBase, General: genBase,
			X: 1, Y: 1, Troops: u32(c.ATroops)}.Write(o))
		die(sangokushi.BattleUnit{Addr: unitBase + 0x40, General: genBase + 0x40,
			X: 2, Y: 1, Troops: u32(c.DTroops)}.Write(o))
		die(sangokushi.SetMobility(o, unitBase, byte(c.AMob)))
		v, err := o.Call(sangokushi.ChargeOdds, unitBase, unitBase+0x40)
		die(err)
		c.OK = int(int32(v))
		f.ChargeOdds = append(f.ChargeOdds, c)
	}

	// ── 距離場 sub_667B0 ──────────────────────────────────────────────
	//
	// 第 0 個盤面是**正對照**：全平地、無單位、無火，目標在 (0,0)。
	// 那時距離場一定是「每走一步 +2」的斜坡。對不上就表示這支工具擺錯了
	// 全域（地形／火／單位格三張表少擺一張都會這樣），不是 remake 錯——
	// 沒有這一格，兩邊不一致的時候分不出是誰的問題。
	f.DistField = append(f.DistField, flatControl(o))
	for i := 0; i < *boards; i++ {
		f.DistField = append(f.DistField, randomBoard(o, rng))
	}
	if err := checkControl(f.DistField[0]); err != nil {
		die(fmt.Errorf("正對照沒過（工具擺錯全域，不是 remake 的問題）：%w", err))
	}

	// ── 相鄰格 sub_63848 ─────────────────────────────────────────────
	//
	// 方向 → 座標的換算是奇偶欄錯半格，**寫錯了只有在某些格子上才看得出來**，
	// 所以整個盤面 × 六個方向全部問一遍。
	for y := 0; y < sangokushi.BattleRows; y++ {
		for x := 0; x < sangokushi.TerrainCols; x++ {
			for dir := 1; dir <= 6; dir++ {
				nx, ny, ok, err := sangokushi.CallNeighbour(o, scratch+0x1800, x, y, dir)
				die(err)
				v := 0
				if ok {
					v = 1
				}
				f.Neighbour = append(f.Neighbour, [6]int{x, y, dir, nx, ny, v})
			}
		}
	}

	// ── 計略／火計成功率 sub_65930／sub_659DE ─────────────────────────
	die(o.SetLong(sangokushi.Difficulty, 1))
	for i := 0; i < 200; i++ {
		c := rateCase{
			AIntel: rng.Intn(101), AExp: rng.Intn(101),
			DIntel: rng.Intn(101), DExp: rng.Intn(101),
		}
		die(sangokushi.General{Addr: genBase, Ruler: 3,
			Intelligence: byte(c.AIntel), Exp: byte(c.AExp)}.Write(o))
		die(sangokushi.General{Addr: genBase + 0x40, Ruler: 7,
			Intelligence: byte(c.DIntel), Exp: byte(c.DExp)}.Write(o))
		die(sangokushi.BattleUnit{Addr: unitBase, General: genBase, X: 1, Y: 1, Troops: 1000}.Write(o))
		die(sangokushi.BattleUnit{Addr: unitBase + 0x40, General: genBase + 0x40, X: 2, Y: 1, Troops: 1000}.Write(o))
		tv, err := o.Call(sangokushi.TrickRate, unitBase, unitBase+0x40)
		die(err)
		fv, err := o.Call(sangokushi.FireRate, unitBase, unitBase+0x40)
		die(err)
		c.Trick, c.Fire = int(int32(tv)), int(int32(fv))
		f.Rates = append(f.Rates, c)
	}

	// ── 單位層決策 sub_670DA ──────────────────────────────────────────
	f.UnitAct = unitActCases(o, rng, *acts)

	// ── 方針層 sub_68382 ──────────────────────────────────────────────
	f.Policy = policyCases(o, rng, *policies)

	// ── 可燃度 sub_6533A ＋ 起火機率 sub_6542A ────────────────────────
	f.Burn = burnCases(o, rng, *burns)

	// ── 名額指派 sub_680E8 ────────────────────────────────────────────
	f.Assign = assignCases(o, rng, *assigns)

	// ── 目標選擇 sub_67968／sub_67EAE／sub_67F22／sub_6788C ────────────
	f.Targets = targetCases(o, rng, *tgts)

	// ── 推進執行者 sub_666FA／sub_67C76／sub_67CEC ─────────────────────
	f.Advance = advCases(o, rng, *advs)

	// ── 整輪 sub_68382 ────────────────────────────────────────────────
	f.Round = roundCases(o, rng, *rounds)

	b, err := json.MarshalIndent(f, "", " ")
	die(err)
	die(os.WriteFile(*out, append(b, '\n'), 0o644))
	fmt.Printf("寫出 %s：六角距離 %d、方向碼 %d、補給 %d、突撃 %d、"+
		"距離場 %d 個盤面、單位層決策 %d\n",
		*out, len(f.HexDistance), len(f.DirCode), len(f.Supply),
		len(f.ChargeOdds), len(f.DistField), len(f.UnitAct))
	fmt.Printf("        相鄰格 %d、成功率 %d、方針 %d、可燃度 %d、名額指派 %d、目標 %d\n",
		len(f.Neighbour), len(f.Rates), len(f.Policy), len(f.Burn),
		len(f.Assign), len(f.Targets))
	fmt.Printf("        推進執行者 %d、整輪 %d\n", len(f.Advance), len(f.Round))
}

// flatControl 全平地、無單位、無火的正對照盤面。
func flatControl(o *oracle.Oracle) fieldCase {
	c := fieldCase{
		Terrain: make([]int, cells), Fire: make([]int, cells), Occupy: make([]int, cells),
		UX: 13, UY: 11, TX: 0, TY: 0,
	}
	return runBoard(o, c)
}

func randomBoard(o *oracle.Oracle, rng *rand.Rand) fieldCase {
	c := fieldCase{
		Terrain: make([]int, cells), Fire: make([]int, cells), Occupy: make([]int, cells),
		Navy:      rng.Intn(2) == 0,
		AvoidFire: rng.Intn(2) == 0,
	}
	for i := range c.Terrain {
		c.Terrain[i] = rng.Intn(6) // 0..5；5 是山（障礙）
	}
	for i := 0; i < 8; i++ {
		c.Fire[rng.Intn(cells)] = rng.Intn(4)
	}
	// 單位：4..9 個，隨機敵我。行動的那一個最後放，確保它站得到。
	n := 4 + rng.Intn(6)
	for i := 0; i < n; i++ {
		p := rng.Intn(cells)
		if c.Occupy[p] != 0 {
			continue
		}
		c.Occupy[p] = 1 + rng.Intn(2)
	}
	up := rng.Intn(cells)
	c.Occupy[up] = 1
	c.UX, c.UY = up%sangokushi.TerrainCols, up/sangokushi.TerrainCols

	// **目標不能放在山上（類型 5）。**
	//
	// 原版把 `field[target]` 無條件歸零（`0x66886`），包括障礙格。歸零之後
	// 那一格就通過了鬆弛的 `field[dst] <= 30000` 檢查，於是成本表被以
	// 「類型 5」查詢——而 `0x770B2` 只有 5 個 long，後面接的是 SJIS 字串。
	// 讀出來的是天文數字，整張場跟著被它汙染（實測 168 格裡 167 格是垃圾）。
	//
	// 這是原版真的存在的越界讀，不是模擬器的問題；但**遊戲裡到不了**：
	// `sub_667B0` 的兩個呼叫端給的目標是敵方單位或城格，單位站不上山，
	// 城格是類型 4。所以 fixture 不產生這種輸入——拿垃圾去對拍，
	// 對到了也沒有意義。
	for {
		t := rng.Intn(cells)
		if c.Terrain[t] <= 4 {
			c.TX, c.TY = t%sangokushi.TerrainCols, t/sangokushi.TerrainCols
			break
		}
	}
	return runBoard(o, c)
}

// runBoard 把盤面寫進記憶體、呼叫距離場、把答案填回去。
func runBoard(o *oracle.Oracle, c fieldCase) fieldCase {
	const ownRuler, enemyRuler = 3, 7
	var b sangokushi.Board
	b.Ruler = ownRuler
	// 每一格的單位各配一個槽與一筆武將記錄——**不能共用一筆**，
	// 因為放寬階段 1 要靠 `G[+0x25]` 分敵我。
	slot := uint32(0)
	for i := 0; i < cells; i++ {
		b.Terrain[i] = byte(c.Terrain[i] << 3)
		b.Fire[i] = byte(c.Fire[i])
		if c.Occupy[i] == 0 {
			continue
		}
		ruler := byte(ownRuler)
		if c.Occupy[i] == 2 {
			ruler = enemyRuler
		}
		g := genBase + slot*0x40
		u := unitBase + slot*0x40
		die(sangokushi.General{Addr: g, Ruler: ruler}.Write(o))
		die(sangokushi.BattleUnit{Addr: u, General: g,
			X: u32(i % sangokushi.TerrainCols), Y: u32(i / sangokushi.TerrainCols),
			Troops: 1000}.Write(o))
		b.Occupy[i] = u
		b.Terrain[i] |= 0x80 // bit7 ＝ 有單位
		slot++
	}
	die(b.Write(o))

	// 行動的那個單位：站在 (UX,UY)，水軍旗標由 Navy 決定。
	// **行動的那個單位也要記進 Occupy**：fixture 要能自己描述整個盤面，
	// 少記它的話讀 fixture 的那一邊會擺出一個「單位不在盤面上」的局面。
	si := c.UY*sangokushi.TerrainCols + c.UX
	self := b.Occupy[si]
	if self == 0 {
		g := genBase + slot*0x40
		self = unitBase + slot*0x40
		die(sangokushi.General{Addr: g, Ruler: ownRuler}.Write(o))
		die(sangokushi.BattleUnit{Addr: self, General: g,
			X: u32(c.UX), Y: u32(c.UY), Troops: 1000}.Write(o))
		b.Occupy[si] = self
		b.Terrain[si] |= 0x80
		c.Occupy[si] = 1
		die(b.Write(o))
	}
	gp, err := o.Long(self)
	die(err)
	if c.Navy {
		die(o.SetByte(gp+sangokushi.GenNavy, 1))
	}
	die(sangokushi.SetMobility(o, self, 20)) // 距離場本身不看，擺著避免帶殘值

	got, err := sangokushi.CallDistField(o, fieldBuf, self, c.TX, c.TY, c.AvoidFire)
	die(err)
	c.Field = make([]int, cells)
	for i, v := range got {
		c.Field[i] = int(v)
	}
	return c
}

// checkControl 正對照：全平地、目標 (0,0)、成本一律 2，所以每一格的距離
// 應該是 2 × 六角距離到 (0,0)。
//
// **唯一的例外是行動單位自己站的那一格**：它是「有單位的格」，放寬階段 0
// 一律當障礙（30001），而六個鄰格都通得過，所以階段 0 就收斂了，
// 那一格不會被放寬。
func checkControl(c fieldCase) error {
	self := c.UY*sangokushi.TerrainCols + c.UX
	for i, v := range c.Field {
		x, y := i%sangokushi.TerrainCols, i/sangokushi.TerrainCols
		want := 2 * hexDist(x, y, 0, 0)
		if i == self {
			want = 30001
		}
		if v != want {
			return fmt.Errorf("(%d,%d) 得到 %d，全平地應該是 %d", x, y, v, want)
		}
	}
	return nil
}

// hexDist 是 AI-0 寫的那條公式，這裡只給正對照用。
func hexDist(x1, y1, x2, y2 int) int {
	dx := abs(x1 - x2)
	dr := abs((2*y1 + x1&1) - (2*y2 + x2&1))
	if dr-dx > 0 {
		return dx + (dr-dx)/2
	}
	return dx
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func u32(v int) uint32 { return uint32(int32(v)) }

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// unitActCases 問原版「這個局面下這個單位會做什麼」。
//
// `sub_670DA` 不回傳決定，它直接執行；所以五個執行者全部攔下來，
// 攔到哪一支就是它決定了什麼（`sangokushi.CaptureAct`）。
func unitActCases(o *oracle.Oracle, rng *rand.Rand, n int) []actCase {
	var act sangokushi.Act
	sangokushi.CaptureAct(o, &act)
	// 亂數固定：`sub_670DA` 這一層不擲骰，固定住是為了「同一組輸入永遠同一個答案」。
	sangokushi.ForceRand(o, func(uint32) uint32 { return 0 })

	out := make([]actCase, 0, n)
	for i := 0; i < n; i++ {
		terrain, fire := make([]byte, cells), make([]byte, cells)
		c := actCase{
			Wind:    rng.Intn(6),
			OwnSup:  rng.Intn(100),
			EnySup:  rng.Intn(100),
			Flags:   rng.Intn(0x40),
			UHuman:  rng.Intn(2) == 0,
			THuman:  rng.Intn(2) == 0,
		}
		// 一半的案例讓補給落在「突撃會被選中」的那一段（我方 ≤ 5 且低於敵方）。
		if i%2 == 0 {
			c.OwnSup, c.EnySup = rng.Intn(6), 6+rng.Intn(60)
		}
		for j := range terrain {
			terrain[j] = byte('0' + rng.Intn(5)) // 0..4，都不是山：山上站不了單位
			fire[j] = '0'
		}
		for j := 0; j < 6; j++ {
			fire[rng.Intn(cells)] = byte('0' + rng.Intn(4))
		}
		c.Terrain, c.Fire = string(terrain), string(fire)
		c.U = randUnit(rng)
		c.T = randUnit(rng)
		// 一半的案例讓兩個單位相鄰——不相鄰的話多數分支都走不到。
		if i%2 == 0 {
			c.T.X, c.T.Y = neighbour(rng, c.U.X, c.U.Y)
		}
		if c.T.X == c.U.X && c.T.Y == c.U.Y {
			continue
		}
		act = sangokushi.Act{Kind: "none"}
		runAct(o, &c)
		c.Kind, c.Mode, c.DX, c.DY = act.Kind, act.Mode, act.X, act.Y
		out = append(out, c)
	}
	return out
}

func randUnit(rng *rand.Rand) unitSpec {
	return unitSpec{
		X: rng.Intn(sangokushi.TerrainCols), Y: rng.Intn(sangokushi.BattleRows),
		Troops:   rng.Intn(12000) + 100,
		Mobility: rng.Intn(16),
		Force:    rng.Intn(100) + 1,
		Intel:    rng.Intn(100) + 1,
		Exp:      rng.Intn(101),
	}
}

// neighbour 六角格的一個相鄰座標（出界就退回原點）。
func neighbour(rng *rand.Rand, x, y int) (int, int) {
	odd := x & 1
	deltas := [6][2]int{{-1, -1 + odd}, {0, -1}, {1, -1 + odd}, {-1, odd}, {0, 1}, {1, odd}}
	d := deltas[rng.Intn(6)]
	nx, ny := x+d[0], y+d[1]
	if nx < 0 || ny < 0 || nx >= sangokushi.TerrainCols || ny >= sangokushi.BattleRows {
		return x, y
	}
	return nx, ny
}

// runAct 擺盤面、呼叫 `sub_670DA`。
func runAct(o *oracle.Oracle, c *actCase) {
	const ownRuler, enemyRuler = 3, 7
	var b sangokushi.Board
	b.Ruler = ownRuler
	for i := 0; i < cells; i++ {
		b.Terrain[i] = (c.Terrain[i] - '0') << 3
		b.Fire[i] = c.Fire[i] - '0'
	}
	ui := c.U.Y*sangokushi.TerrainCols + c.U.X
	ti := c.T.Y*sangokushi.TerrainCols + c.T.X
	b.Occupy[ui] = unitBase
	b.Occupy[ti] = unitBase + 0x40
	b.Terrain[ui] |= 0x80 // bit7 ＝ 有單位
	b.Terrain[ti] |= 0x80
	die(b.Write(o))
	die(o.SetLong(sangokushi.Wind, u32(c.Wind)))
	die(o.SetLong(sangokushi.OwnSupply, u32(c.OwnSup)))
	die(o.SetLong(sangokushi.EnemySupply, u32(c.EnySup)))
	die(o.SetLong(sangokushi.PolicyFlag, 0))
	die(o.SetLong(sangokushi.ActingRuler, ownRuler))
	// **難度要設 1**：補正 ＝ 難度 − 1，設 1 之後雙方的補正都是 0，
	// 計略／火計成功率就只剩「能力差」那一項。不設的話記憶體裡是殘值，
	// 兩邊的補正不同，算出來的成功率與 remake 對不上——而那看起來像
	// 「remake 的判斷寫錯了」，其實是盤面沒擺乾淨。
	die(o.SetLong(sangokushi.Difficulty, 1))
	// 君主表的「人類玩家」bit0：`sub_6533A` 的難度補正正負號看它。
	die(sangokushi.SetRulerHuman(o, ownRuler, c.UHuman))
	die(sangokushi.SetRulerHuman(o, enemyRuler, c.THuman))

	writeUnit(o, unitBase, genBase, ownRuler, c.U)
	writeUnit(o, unitBase+0x40, genBase+0x40, enemyRuler, c.T)
	if _, err := o.Call(sangokushi.UnitAct, unitBase, unitBase+0x40, u32(c.Flags)); err != nil {
		die(err)
	}
}

func writeUnit(o *oracle.Oracle, unit, gen uint32, ruler byte, s unitSpec) {
	die(sangokushi.General{Addr: gen, Ruler: ruler,
		Strength: byte(s.Force), Intelligence: byte(s.Intel), Exp: byte(s.Exp)}.Write(o))
	die(sangokushi.BattleUnit{Addr: unit, General: gen,
		X: u32(s.X), Y: u32(s.Y), Troops: u32(s.Troops)}.Write(o))
	die(sangokushi.SetMobility(o, unit, byte(s.Mobility)))
}

// policyCases 問原版「這個局面下這一方會用哪個方針」。
//
// `sub_68382` 不吃參數，四個量與五種方針全部由全域決定；「讀世界」的五支
// 攔掉由這裡給值（`sangokushi.CapturePolicy`）。
func policyCases(o *oracle.Oracle, rng *rand.Rand, n int) []policyCase {
	const defRuler, atkRuler = 3, 7
	var w sangokushi.World
	var pick sangokushi.PolicyPick
	sangokushi.CapturePolicy(o, &w, &pick)

	// 國記錄與對方槽陣列：擺一塊全 0 的就好——金米走 SupplyScore（已攔），
	// 對方槽全 0 表示「找不到君主本人／総大将」，斬首那一條因此不會觸發。
	// **這一批不涵蓋斬首**，它要的是一整份場上狀態。
	for i := uint32(0); i < 0x100; i += 4 {
		die(o.SetLong(scratch+0x2000+i, 0))
		die(o.SetLong(scratch+0x2200+i, 0))
	}
	die(o.SetLong(sangokushi.NationPtr, scratch+0x2000))
	die(o.SetLong(sangokushi.OppSlots, scratch+0x2200))
	die(o.SetLong(sangokushi.HQX, 6))
	die(o.SetLong(sangokushi.HQY, 5))
	// 盤面：全平地、沒有單位。主力那一條會看本陣格上站著誰。
	var b sangokushi.Board
	b.Ruler = defRuler
	die(b.Write(o))

	out := make([]policyCase, 0, n)
	for i := 0; i < n; i++ {
		c := policyCase{
			Day: rng.Intn(28) + 1, Groups: rng.Intn(4) + 1,
			OwnSup: rng.Intn(40), EnySup: rng.Intn(40),
			OwnGens: rng.Intn(6) + 1, EnyGens: rng.Intn(6) + 1,
			OwnTroops: rng.Intn(40000) + 100, EnyTroops: rng.Intn(40000) + 100,
			HasRetreat: rng.Intn(2) == 0,
		}
		// 可上場人數不會超過該君主在戰場所在國的武將數。
		c.OwnDeploy = rng.Intn(c.OwnGens + 1)
		c.EnyDeploy = rng.Intn(c.EnyGens + 1)
		acting, opp := byte(defRuler), byte(atkRuler)
		c.Acting = "defender"
		if i%2 == 1 {
			acting, opp = byte(atkRuler), byte(defRuler)
			c.Acting = "attacker"
		}
		w = sangokushi.World{
			Supply:      map[byte]uint32{defRuler: 0, atkRuler: 0},
			Deployable:  map[byte]uint32{},
			NationGens:  map[byte]uint32{},
			TotalTroops: map[byte]uint32{},
		}
		// 補給：`sub_68302` 的第三個參數，守方那次傳 0x7761A、攻方那次傳 0x77616。
		w.Supply[byte(defRuler)] = u32(c.OwnSup)
		w.Supply[byte(atkRuler)] = u32(c.EnySup)
		if c.Acting == "attacker" { // 交換之後行動方是攻方
			w.Supply[byte(defRuler)] = u32(c.EnySup)
			w.Supply[byte(atkRuler)] = u32(c.OwnSup)
		}
		w.Deployable[acting] = u32(c.OwnDeploy)
		w.Deployable[opp] = u32(c.EnyDeploy)
		w.NationGens[acting] = u32(c.OwnGens)
		w.NationGens[opp] = u32(c.EnyGens)
		w.TotalTroops[acting] = u32(c.OwnTroops)
		w.TotalTroops[opp] = u32(c.EnyTroops)
		if c.HasRetreat {
			w.Retreat = scratch + 0x2000
		} else {
			w.Retreat = 0
		}

		die(o.SetLong(sangokushi.DefRuler, defRuler))
		die(o.SetLong(sangokushi.AtkRuler, atkRuler))
		die(o.SetLong(sangokushi.ActingRuler, uint32(acting)))
		die(o.SetLong(sangokushi.OppRuler, uint32(opp)))
		die(o.SetLong(sangokushi.BattleDay, u32(c.Day)))
		die(o.SetLong(sangokushi.CastleGroups, u32(c.Groups)))

		pick = sangokushi.PolicyPick{Kind: "none"}
		if _, err := o.Call(sangokushi.PolicyTurn); err != nil {
			die(err)
		}
		c.Kind, c.N = pick.Kind, pick.N
		out = append(out, c)
	}
	return out
}

// burnCases 問原版「這一格多好燒、這一回合起火機率多少」。
//
// 兩支都是純函式：吃全域盤面，回一個數字，不動任何東西。
func burnCases(o *oracle.Oracle, rng *rand.Rand, n int) []burnCase {
	const ruler = 3
	out := make([]burnCase, 0, n)
	for i := 0; i < n; i++ {
		terrain, fire := make([]byte, cells), make([]byte, cells)
		c := burnCase{
			Wind:    rng.Intn(6),
			X:       rng.Intn(sangokushi.TerrainCols),
			Y:       rng.Intn(sangokushi.BattleRows),
			CPU:     rng.Intn(10) + 1,
			HasUnit: rng.Intn(4) != 0,
			Intel:   rng.Intn(101),
			Exp:     rng.Intn(101),
			Human:   rng.Intn(2) == 0,
		}
		for j := range terrain {
			terrain[j] = byte('0' + rng.Intn(8)) // 0..7：可燃度表就是 8 項，全部走一遍
		}
		// 起火機率只看順風三個鄰格的計數**是不是恰好 3**（新火），撒得太稀
		// 的話 400 例裡有 370 例是 0，等於沒驗到累加那一段。這裡讓 3 佔多數，
		// 另外留 1／2 進去，累加條件寫成 `!= 0` 的話會被抓出來。
		fireMix := "0003312"
		for j := range fire {
			fire[j] = fireMix[rng.Intn(len(fireMix))]
		}
		// 目標格自己有一半機率是沒火的——有火的話兩支都直接回 0，驗不到後面。
		if rng.Intn(2) == 0 {
			fire[c.Y*sangokushi.TerrainCols+c.X] = '0'
		}
		c.Terrain, c.Fire = string(terrain), string(fire)

		var b sangokushi.Board
		b.Ruler = ruler
		for j := 0; j < cells; j++ {
			b.Terrain[j] = (c.Terrain[j] - '0') << 3
			b.Fire[j] = c.Fire[j] - '0'
		}
		if c.HasUnit {
			b.Occupy[c.Y*sangokushi.TerrainCols+c.X] = unitBase
			b.Terrain[c.Y*sangokushi.TerrainCols+c.X] |= 0x80 // bit7 ＝ 有單位
		}
		die(b.Write(o))
		die(o.SetLong(sangokushi.Wind, u32(c.Wind)))
		die(o.SetLong(sangokushi.Difficulty, u32(c.CPU)))
		die(sangokushi.SetRulerHuman(o, ruler, c.Human))
		die(sangokushi.General{Addr: genBase, Ruler: ruler,
			Intelligence: byte(c.Intel), Exp: byte(c.Exp)}.Write(o))
		die(sangokushi.BattleUnit{Addr: unitBase, General: genBase,
			X: u32(c.X), Y: u32(c.Y), Troops: 1000}.Write(o))

		bv, err := o.Call(sangokushi.Burnability, u32(c.X), u32(c.Y))
		die(err)
		sv, err := o.Call(sangokushi.SpreadChance, u32(c.X), u32(c.Y))
		die(err)
		c.Burn, c.Spread = int(int32(bv)), int(int32(sv))
		out = append(out, c)
	}
	return out
}

// assignCases 問原版「這十個槽，誰走主目標、誰走推進目標」。
//
// `sub_680E8` 本體只有排名與名額兩件事，其餘全是呼叫別人；把那些全部攔掉
// 之後它就是純函式（`sangokushi.CaptureAssign`）。這一層對不上的症狀不會
// 出現在畫面上——單位還是會動，只是動去別的地方。
func assignCases(o *oracle.Oracle, rng *rand.Rand, n int) []assignCase {
	var w sangokushi.AssignWorld
	var steps []sangokushi.AssignStep
	sangokushi.CaptureAssign(o, slotBase, stubTarget, stubAct, &w, &steps)

	out := make([]assignCase, 0, n)
	for i := 0; i < n; i++ {
		c := assignCase{
			X:      make([]int, 10),
			Y:      make([]int, 10),
			Filled: make([]bool, 10),
			N:      rng.Intn(7),
			AX:     rng.Intn(sangokushi.TerrainCols),
			AY:     rng.Intn(sangokushi.BattleRows),
			BX:     rng.Intn(sangokushi.TerrainCols),
			BY:     rng.Intn(sangokushi.BattleRows),
			HasA:   rng.Intn(8) != 0, // 少數案例讓 cb_target 說「沒有目標」
		}
		for j := 0; j < 10; j++ {
			c.X[j] = rng.Intn(sangokushi.TerrainCols)
			c.Y[j] = rng.Intn(sangokushi.BattleRows)
			c.Filled[j] = rng.Intn(4) != 0
		}
		// 一半的案例把所有單位塞在同一條線上：排名的平手拆解
		// （距離相同時比對主目標的距離）只有在平手夠多時才驗得到。
		if i%2 == 0 {
			row := rng.Intn(sangokushi.BattleRows)
			for j := 0; j < 10; j++ {
				c.Y[j] = row
			}
		}
		die(sangokushi.WriteAssignSlots(o, slotBase, c.X, c.Y, c.Filled))
		w = sangokushi.AssignWorld{BX: c.BX, BY: c.BY, AX: c.AX, AY: c.AY, HasA: c.HasA}
		steps = nil
		if _, err := o.Call(sangokushi.Assign, stubTarget, stubAct, u32(c.N)); err != nil {
			die(err)
		}
		c.Steps = steps
		out = append(out, c)
	}
	return out
}

// targetCases 問原版「這個局面下，AI 的兩個目標各是哪一格」。
//
// 四支都是純函式（吃全域盤面、回一組座標），但它們共用的城格清單
// `0x77102` 不是——那是 `sub_64718` 開場建好的。這裡用
// `sangokushi.BuildCastleList` 重建（配對那一步呼叫原版自己的 `sub_645FE`），
// 建完的 20 bytes 一起寫進 fixture：remake 既要能**重建同一份清單**，
// 也要在同一份清單上選出同一格。
func targetCases(o *oracle.Oracle, rng *rand.Rand, n int) []targetCase {
	const defRuler, atkRuler = 3, 7
	out := make([]targetCase, 0, n)
	for i := 0; i < n; i++ {
		c := targetCase{Navy: rng.Intn(2) == 0, Acting: "defender"}
		acting := byte(defRuler)
		if i%2 == 1 {
			c.Acting, acting = "attacker", byte(atkRuler)
		}
		terrain := make([]byte, cells)
		fire := make([]byte, cells)
		units := make([]byte, cells)
		for j := range terrain {
			terrain[j] = byte('0' + rng.Intn(6)) // 0..5，5 是山
			fire[j] = '0'
			units[j] = '.'
		}
		for j := 0; j < 6; j++ {
			fire[rng.Intn(cells)] = byte('0' + rng.Intn(4))
		}
		// 城格 1..8 個（`sub_64718` 最多收 10 個）。
		for j := 0; j < 1+rng.Intn(8); j++ {
			terrain[rng.Intn(cells)] = '4'
		}
		// 四分之一的攻方案例排成**相鄰的城格對**：`sub_67968` 的第三候選
		// （保底）只有在「城格空著、但配對格被自己人佔住」時才用得到，
		// 而那需要城格真的有配對。
		pairedBoard := c.Acting == "attacker" && rng.Intn(4) == 0
		if pairedBoard {
			for j := range terrain {
				if terrain[j] == '4' {
					terrain[j] = '0'
				}
			}
			for j := 0; j < 2+rng.Intn(3); j++ {
				x, y := rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
				nx, ny, ok, err := sangokushi.CallNeighbour(o, scratch+0x1880, x, y, 1+rng.Intn(6))
				die(err)
				if !ok {
					continue
				}
				terrain[y*sangokushi.TerrainCols+x] = '4'
				terrain[ny*sangokushi.TerrainCols+nx] = '4'
			}
		}
		// 本陣：一格物件碼 6（不放在城格上）。
		for {
			p := rng.Intn(cells)
			if terrain[p] != '4' {
				c.HQX, c.HQY = p%sangokushi.TerrainCols, p/sangokushi.TerrainCols
				break
			}
		}
		// 單位：兩邊各 0..10 個，不站山上。
		// 槽 0 留給行動單位，其餘從 1 起。**一個槽只能出現在一格**——
		// 同一個槽寫進兩格的話，原版的單位格陣列會在兩處都指到它，
		// 而它自己的座標只有一個，盤面就自相矛盾了。
		nextDef, nextAtk := 1, 1
		place := func(mark byte, next *int, k int) {
			for j := 0; j < k && *next < 10; j++ {
				p := rng.Intn(cells)
				if units[p] != '.' || terrain[p] == '5' {
					continue
				}
				units[p] = mark + byte(*next)
				*next++
			}
		}
		place('a', &nextDef, rng.Intn(10))
		place('A', &nextAtk, rng.Intn(10))
		// 三分之一的案例把守方單位塞滿城格：`sub_67968` 的第二候選
		// （守方佔著的城格裡 `sub_6788C` 最高的）只有在**沒有空城格**時才用得到，
		// 隨機盤面幾乎踩不到，等於沒驗。
		if !pairedBoard && rng.Intn(3) == 0 {
			for j := range terrain {
				if terrain[j] == '4' && units[j] == '.' && nextDef < 10 {
					units[j] = 'a' + byte(nextDef)
					nextDef++
				}
			}
		}
		// 配對盤面：城格上只放**攻方**（行動方）單位，而且隔一格放一個——
		// 守方一站上去就會命中第二候選，第三候選還是驗不到。
		if pairedBoard {
			for j := range units {
				if terrain[j] == '4' && units[j] >= 'a' && units[j] <= 'j' {
					units[j] = '.'
				}
			}
			k := 0
			for j := range terrain {
				if terrain[j] == '4' && units[j] == '.' && nextAtk < 10 {
					if k%2 == 0 {
						units[j] = 'A' + byte(nextAtk)
						nextAtk++
					}
					k++
				}
			}
		}
		selfMark := byte('a')
		if c.Acting == "attacker" {
			selfMark = 'A'
		}
		for {
			p := rng.Intn(cells)
			if terrain[p] == '5' || units[p] != '.' {
				continue
			}
			units[p] = selfMark
			c.UX, c.UY = p%sangokushi.TerrainCols, p/sangokushi.TerrainCols
			break
		}
		for j := 0; j < 20; j++ {
			if rng.Intn(12) == 0 {
				c.Lords = append(c.Lords, j)
			}
			if rng.Intn(12) == 0 {
				c.Chiefs = append(c.Chiefs, j)
			}
			if rng.Intn(4) == 0 {
				c.Acted = append(c.Acted, j)
			}
		}
		c.Terrain, c.Fire, c.Units = string(terrain), string(fire), string(units)

		self32 := writeTargetBoard(o, &c, acting)
		die(sangokushi.BuildCastleList(o, adjBuf, scratch+0x1880))
		cellsB, pairsB, groups, err := sangokushi.ReadCastleList(o)
		die(err)
		c.Cells, c.Pairs, c.Groups = cellsB, pairsB, groups

		c.Threat = make([]int, cells)
		for j := 0; j < cells; j++ {
			v, err := o.Call(sangokushi.ThreatAt,
				u32(j%sangokushi.TerrainCols), u32(j/sangokushi.TerrainCols), defRuler)
			die(err)
			c.Threat[j] = int(int32(v))
		}
		ask := func(addr uint32, args ...uint32) [3]int {
			// `sub_667B0` 開頭會改 `0x770B2` 的第 3 項且不還原，
			// 每次呼叫之前都要設回去（不然第二次算出來的距離場是錯的）。
			die(sangokushi.ResetStepCost(o))
			die(o.SetLong(scratch+0x1890, 0))
			die(o.SetLong(scratch+0x1894, 0))
			a := append([]uint32{scratch + 0x1890, scratch + 0x1894}, args...)
			r, err := o.Call(addr, a...)
			die(err)
			x, err := o.Long(scratch + 0x1890)
			die(err)
			y, err := o.Long(scratch + 0x1894)
			die(err)
			return [3]int{int(int32(x)), int(int32(y)), int(int32(r))}
		}
		c.T0 = ask(sangokushi.PickTarget, 0, self32)
		c.T1 = ask(sangokushi.PickTarget, 1, self32)
		c.LC = ask(sangokushi.LordCell, self32)
		c.BT = ask(sangokushi.BestThreat, self32)
		out = append(out, c)
	}
	return out
}

// writeTargetBoard 擺盤面、兩邊各十個槽，回傳行動單位的槽位址。
func writeTargetBoard(o *oracle.Oracle, c *targetCase, acting byte) uint32 {
	const defRuler, atkRuler = 3, 7
	var b sangokushi.Board
	b.Ruler = acting
	for j := 0; j < cells; j++ {
		b.Terrain[j] = (c.Terrain[j] - '0') << 3
		b.Fire[j] = c.Fire[j] - '0'
	}
	b.Terrain[c.HQY*sangokushi.TerrainCols+c.HQX] |= 6 // 物件碼 6 ＝ 本陣

	has := func(list []int, v int) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	// 兩邊各十個槽全部先清空，再依 units 字串填。
	for k := 0; k < 10; k++ {
		die(sangokushi.BattleUnit{Addr: defSlots + uint32(k)*sangokushi.SlotStride}.Write(o))
		die(sangokushi.BattleUnit{Addr: atkSlots + uint32(k)*sangokushi.SlotStride}.Write(o))
	}
	self := uint32(0)
	for j := 0; j < cells; j++ {
		ch := c.Units[j]
		if ch == '.' {
			continue
		}
		var slot, gen uint32
		var ruler byte
		var idx int
		if ch >= 'a' && ch <= 'j' {
			idx = int(ch - 'a')
			slot, gen, ruler = defSlots+uint32(idx)*sangokushi.SlotStride, defGens+uint32(idx)*0x30, defRuler
		} else {
			idx = 10 + int(ch-'A')
			k := idx - 10
			slot, gen, ruler = atkSlots+uint32(k)*sangokushi.SlotStride, atkGens+uint32(k)*0x30, atkRuler
		}
		g := sangokushi.General{Addr: gen, Ruler: ruler, Intelligence: 50, Strength: 50}
		die(g.Write(o))
		if has(c.Lords, idx) {
			die(o.SetByte(gen+0x1B, 0x02))
		}
		if has(c.Chiefs, idx) {
			die(o.SetByte(gen+0x2A, 0x80))
		}
		die(sangokushi.BattleUnit{Addr: slot, General: gen,
			X: u32(j % sangokushi.TerrainCols), Y: u32(j / sangokushi.TerrainCols),
			Troops: 1000}.Write(o))
		mob := byte(6)
		if has(c.Acted, idx) {
			mob = 0xFF
		}
		die(sangokushi.SetMobility(o, slot, mob))
		b.Occupy[j] = slot
		b.Terrain[j] |= 0x80 // bit7 ＝ 有單位（`sub_662E2`／`sub_6533A` 都看它）
		if j == c.UY*sangokushi.TerrainCols+c.UX {
			self = slot
			if c.Navy {
				die(o.SetByte(gen+0x1B, 0x01))
			}
		}
	}
	die(b.Write(o))
	die(o.SetLong(sangokushi.DefRuler, defRuler))
	die(o.SetLong(sangokushi.AtkRuler, atkRuler))
	die(o.SetLong(sangokushi.ActingRuler, uint32(acting)))
	opp := uint32(atkRuler)
	oppSlots := uint32(atkSlots)
	if acting == atkRuler {
		opp, oppSlots = defRuler, defSlots
	}
	die(o.SetLong(sangokushi.OppRuler, opp))
	die(o.SetLong(sangokushi.OppSlots, oppSlots))
	die(o.SetLong(sangokushi.HQX, u32(c.HQX)))
	die(o.SetLong(sangokushi.HQY, u32(c.HQY)))
	die(o.SetLong(sangokushi.Wind, 0))
	die(sangokushi.ResetStepCost(o))
	return self
}

// advCases 問原版「拿到目標之後怎麼動」。
//
// `sub_67C76`（推進目標）與 `sub_67CEC`（主目標）都只有兩條路：目標格空著
// 就走過去（`sub_66FA8`），有人就交給單位層（`sub_670DA`）。兩支執行者
// 攔下來就知道走了哪一條、帶什麼參數。
//
// `sub_666FA` 決定「打誰」——它不是「兵最多的那個」，而是
// 「還沒動的敵方総大将優先，其次照風向優先序取第一個敵人」。
func advCases(o *oracle.Oracle, rng *rand.Rand, n int) []advCase {
	const defRuler, atkRuler = 3, 7
	var kind string
	var gx, gy, genemy, gflags int
	slotOf := func(v uint32) int {
		switch {
		case v >= defSlots && v < defSlots+10*sangokushi.SlotStride:
			return int(v-defSlots) / sangokushi.SlotStride
		case v >= atkSlots && v < atkSlots+10*sangokushi.SlotStride:
			return 10 + int(v-atkSlots)/sangokushi.SlotStride
		}
		return -1
	}
	arg := func(f *x68k.Frame, i int) int {
		v, err := xc.Long(f, i)
		if err != nil {
			return 0
		}
		return int(int32(v))
	}
	// `CaptureAssign` 在 `AdvanceTo` 上裝過「記一步就回去」的攔截點，
	// 這裡要的是它真的跑，換成不略過原函式的那種。
	o.Intercept(sangokushi.AdvanceTo, func(*x68k.Frame) (uint32, bool) { return 0, false })
	o.Intercept(sangokushi.ActApproach, func(f *x68k.Frame) (uint32, bool) {
		kind, gx, gy, gflags = "move", arg(f, 1), arg(f, 2), arg(f, 3)
		return 0, true
	})
	o.Intercept(sangokushi.UnitAct, func(f *x68k.Frame) (uint32, bool) {
		kind, genemy, gflags = "act", slotOf(uint32(arg(f, 1))), arg(f, 2)
		return 0, true
	})

	out := make([]advCase, 0, n)
	for i := 0; i < n; i++ {
		var c advCase
		tc := targetCase{Acting: "defender"}
		acting := byte(defRuler)
		if i%2 == 1 {
			tc.Acting, acting = "attacker", byte(atkRuler)
		}
		terrain := make([]byte, cells)
		fire := make([]byte, cells)
		units := make([]byte, cells)
		for j := range terrain {
			terrain[j] = byte('0' + rng.Intn(5))
			fire[j] = '0'
			units[j] = '.'
		}
		for j := 0; j < 4; j++ {
			fire[rng.Intn(cells)] = byte('0' + rng.Intn(4))
		}
		nextDef, nextAtk := 1, 1
		for j := 0; j < 8 && nextDef < 10; j++ {
			p := rng.Intn(cells)
			if units[p] == '.' {
				units[p] = 'a' + byte(nextDef)
				nextDef++
			}
		}
		for j := 0; j < 8 && nextAtk < 10; j++ {
			p := rng.Intn(cells)
			if units[p] == '.' {
				units[p] = 'A' + byte(nextAtk)
				nextAtk++
			}
		}
		selfMark := byte('a')
		if tc.Acting == "attacker" {
			selfMark = 'A'
		}
		for {
			p := rng.Intn(cells)
			if units[p] != '.' {
				continue
			}
			units[p] = selfMark
			tc.UX, tc.UY = p%sangokushi.TerrainCols, p/sangokushi.TerrainCols
			break
		}
		// 一半的案例把目標放在自己旁邊——不然幾乎都走「走過去」那條。
		tc.HQX, tc.HQY = rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
		bx, by := rng.Intn(sangokushi.TerrainCols), rng.Intn(sangokushi.BattleRows)
		if i%2 == 0 {
			nx, ny, ok, err := sangokushi.CallNeighbour(o, scratch+0x1880, tc.UX, tc.UY, 1+rng.Intn(6))
			die(err)
			if ok {
				bx, by = nx, ny
			}
		}
		for j := 0; j < 20; j++ {
			if rng.Intn(6) == 0 {
				tc.Chiefs = append(tc.Chiefs, j)
			}
			if rng.Intn(4) == 0 {
				tc.Acted = append(tc.Acted, j)
			}
		}
		tc.Terrain, tc.Fire, tc.Units = string(terrain), string(fire), string(units)
		c = advCase{Terrain: tc.Terrain, Fire: tc.Fire, Units: tc.Units,
			Wind: rng.Intn(6), Chiefs: tc.Chiefs, Acted: tc.Acted,
			Acting: tc.Acting, UX: tc.UX, UY: tc.UY, Flags: rng.Intn(0x40),
			BX: bx, BY: by}

		self := writeTargetBoard(o, &tc, acting)
		die(o.SetLong(sangokushi.Wind, u32(c.Wind)))

		e, err := o.Call(sangokushi.PickEnemy, self, 0)
		die(err)
		c.Enemy = -1
		if e != 0 {
			c.Enemy = slotOf(e)
		}

		kind, gx, gy, genemy, gflags = "none", 0, 0, -1, 0
		if _, err := o.Call(sangokushi.AdvanceTo, self, u32(bx), u32(by), u32(c.Flags)); err != nil {
			die(err)
		}
		c.C76Kind, c.C76X, c.C76Y, c.C76Enemy, c.C76Flags = kind, gx, gy, genemy, gflags

		kind, gx, gy, genemy, gflags = "none", 0, 0, -1, 0
		if _, err := o.Call(sangokushi.MainAct, self, u32(bx), u32(by)); err != nil {
			die(err)
		}
		c.CECKind, c.CECEnemy, c.CECFlags = kind, genemy, gflags
		out = append(out, c)
	}
	return out
}

// roundCases 問原版「這一整輪，每個單位各做了什麼、最後站在哪」。
//
// 這是把方針層、名額指派、目標選擇、推進執行者、單位層**串起來**跑一次：
// 從 `sub_68382` 進去，一路到每個單位動完。走位是真的走（`sub_66FA8` →
// `sub_667B0` → `sub_65030` 全程執行），所以盤面會隨著前面的單位改變，
// 後面的單位看到的是變過的盤面——這正是「串起來」才驗得到的東西。
//
// 攔掉的只有四類，每一類都有理由：
//
//   - 畫面與節奏（`sub_5BBA8` 游標、`0x5C042` 訊息列、`sub_634AA` 面板、
//     `sub_61572` 忙等）：無頭環境沒有它們，`sub_61572` 不攔會空轉幾百萬圈。
//   - 出手結算 `loc_65B5A`：**記下來就跳過**。remake 的 `BattleTurn` 也只回報
//     mode 與對象、不套用損失，攔掉才是同一件事。
//   - 讀世界的四支（`sub_63196`／`sub_631EC`／`sub_6342A`／`sub_622CC`）：
//     由呼叫端給值，remake 那邊擺出產生同樣數字的狀態。
//   - `sub_674E2`（緊急處置）、`sub_64F42`（奪糧）、`sub_66536`（結束判定）：
//     remake 的 `BattleTurn` 沒有這三步，讓它們回 0 才比得到同一件事。
//     **這是這一批的已知範圍，不是「原版沒有這三步」。**
func roundCases(o *oracle.Oracle, rng *rand.Rand, n int) []roundCase {
	const defRuler, atkRuler = 3, 7
	var strikes []strikeRec
	slotOf := func(v uint32) int {
		switch {
		case v >= defSlots && v < defSlots+10*sangokushi.SlotStride:
			return int(v-defSlots) / sangokushi.SlotStride
		case v >= atkSlots && v < atkSlots+10*sangokushi.SlotStride:
			return 10 + int(v-atkSlots)/sangokushi.SlotStride
		}
		return -1
	}
	argL := func(f *x68k.Frame, i int) int {
		v, err := xc.Long(f, i)
		if err != nil {
			return 0
		}
		return int(int32(v))
	}
	mute := func(a uint32) {
		o.Intercept(a, func(*x68k.Frame) (uint32, bool) { return 0, true })
	}
	for _, a := range []uint32{sangokushi.CursorTo, sangokushi.BattleMsg,
		sangokushi.PanelDraw, sangokushi.BusyWait, sangokushi.Emerg,
		sangokushi.SeizeCheck, sangokushi.OverCheck, sangokushi.RetreatTo} {
		mute(a)
	}
	// `CaptureAssign`／`CapturePolicy` 在這幾支上裝過會略過原函式的攔截點，
	// 整輪要它們真的跑。
	for _, a := range []uint32{sangokushi.SupplyScore, sangokushi.Assign, sangokushi.AdvanceTo,
		sangokushi.PolForage, sangokushi.PolDecapit, sangokushi.PolMainAtk,
		sangokushi.PolAttrition, sangokushi.PolCollapse, sangokushi.ActApproach,
		sangokushi.UnitAct, sangokushi.CounterAtk} {
		o.Intercept(a, func(*x68k.Frame) (uint32, bool) { return 0, false })
	}
	o.Intercept(sangokushi.ActStrike, func(f *x68k.Frame) (uint32, bool) {
		strikes = append(strikes, strikeRec{U: slotOf(uint32(argL(f, 0))),
			T: slotOf(uint32(argL(f, 1))), Mode: argL(f, 2)})
		return 0, true
	})
	// 方針與名額只看不改——整輪要它們真的跑。
	policy, quota := "none", -1
	for a, nm := range map[uint32]string{
		sangokushi.PolAttrition: "attrition", sangokushi.PolCollapse: "collapse",
		sangokushi.PolForage: "forage", sangokushi.PolDecapit: "decapit",
		sangokushi.PolMainAtk: "main-atk",
	} {
		nm := nm
		o.OnCall(a, func(*x68k.Frame) { policy = nm })
	}
	o.OnCall(sangokushi.Assign, func(f *x68k.Frame) { quota = argL(f, 2) })
	var world sangokushi.World
	pick := func(m map[byte]uint32, k byte) uint32 {
		if m == nil {
			return 0
		}
		return m[k]
	}
	argByte := func(f *x68k.Frame, i int) byte { return byte(argL(f, i)) }
	o.Intercept(sangokushi.Deployable, func(f *x68k.Frame) (uint32, bool) {
		return pick(world.Deployable, argByte(f, 0)), true
	})
	o.Intercept(sangokushi.NationGens, func(f *x68k.Frame) (uint32, bool) {
		return pick(world.NationGens, argByte(f, 0)), true
	})
	o.Intercept(sangokushi.TotalTroops, func(f *x68k.Frame) (uint32, bool) {
		return pick(world.TotalTroops, argByte(f, 0)), true
	})
	sangokushi.ForceRand(o, func(uint32) uint32 { return 0 })

	out := make([]roundCase, 0, n)
	for i := 0; i < n; i++ {
		c := roundCase{
			Troops: make([]int, 20), Mob: make([]int, 20),
			Intel: make([]int, 20), Force: make([]int, 20),
			Wind: rng.Intn(6), Day: 1 + rng.Intn(27),
			Acting:  "defender",
			Gold:    rng.Intn(20000) + 500,
			Rice:    rng.Intn(20000) + 500,
			AtkGold: rng.Intn(20000) + 500,
			AtkRice: rng.Intn(20000) + 500,
			OwnGens: 1 + rng.Intn(6), EnyGens: 1 + rng.Intn(6),
		}
		acting := byte(defRuler)
		if i%2 == 1 {
			c.Acting, acting = "attacker", byte(atkRuler)
		}
		c.OwnDeploy, c.EnyDeploy = rng.Intn(c.OwnGens+1), rng.Intn(c.EnyGens+1)

		terrain := make([]byte, cells)
		fire := make([]byte, cells)
		units := make([]byte, cells)
		for j := range terrain {
			terrain[j] = byte('0' + rng.Intn(5))
			fire[j] = '0'
			units[j] = '.'
		}
		for j := 0; j < 3; j++ {
			fire[rng.Intn(cells)] = byte('0' + rng.Intn(4))
		}
		for j := 0; j < 1+rng.Intn(5); j++ {
			terrain[rng.Intn(cells)] = '4'
		}
		for {
			p := rng.Intn(cells)
			if terrain[p] != '4' {
				c.HQX, c.HQY = p%sangokushi.TerrainCols, p/sangokushi.TerrainCols
				break
			}
		}
		// 兩邊各自擠在一個中心附近、兩個中心相鄰——散在全場的話幾乎不會接戰，
		// 而「串起來」要驗的正是接戰之後的決定。
		cx, cy := 2+rng.Intn(10), 2+rng.Intn(8)
		place := func(mark byte, k, ox, oy int) {
			for j := 0; j < k; j++ {
				p := (oy+rng.Intn(3)-1)*sangokushi.TerrainCols + ox + rng.Intn(3) - 1
				if p < 0 || p >= cells || units[p] != '.' || terrain[p] == '5' {
					continue
				}
				units[p] = mark + byte(j)
			}
		}
		place('a', 1+rng.Intn(5), cx, cy)
		place('A', 1+rng.Intn(5), cx+1, cy+1)
		for j := 0; j < 20; j++ {
			c.Troops[j] = 300 + rng.Intn(9000)
			c.Mob[j] = 2 + rng.Intn(10)
			c.Intel[j] = 1 + rng.Intn(100)
			c.Force[j] = 1 + rng.Intn(100)
			if rng.Intn(10) == 0 {
				c.Chiefs = append(c.Chiefs, j)
			}
			if rng.Intn(14) == 0 {
				c.Lords = append(c.Lords, j)
			}
		}
		c.Terrain, c.Fire, c.Units = string(terrain), string(fire), string(units)

		ownT, enyT := writeRoundBoard(o, &c, acting)
		world = sangokushi.World{
			Deployable:  map[byte]uint32{},
			NationGens:  map[byte]uint32{},
			TotalTroops: map[byte]uint32{},
		}
		opp := byte(atkRuler)
		if acting == atkRuler {
			opp = defRuler
		}
		world.Deployable[acting] = u32(c.OwnDeploy)
		world.Deployable[opp] = u32(c.EnyDeploy)
		world.NationGens[acting] = u32(c.OwnGens)
		world.NationGens[opp] = u32(c.EnyGens)
		world.TotalTroops[acting] = u32(ownT)
		world.TotalTroops[opp] = u32(enyT)

		cellsB, pairsB, groups, err := sangokushi.ReadCastleList(o)
		die(err)
		_, _ = cellsB, pairsB
		c.Groups = groups

		strikes, policy, quota = nil, "none", -1
		if _, err := o.Call(sangokushi.PolicyTurn); err != nil {
			die(fmt.Errorf("整輪案例 %d：%w", i, err))
		}
		c.Strikes, c.Policy, c.Quota = strikes, policy, quota
		c.FinalX, c.FinalY, c.FinalM = make([]int, 20), make([]int, 20), make([]int, 20)
		for k := 0; k < 20; k++ {
			base := uint32(defSlots) + uint32(k)*sangokushi.SlotStride
			if k >= 10 {
				base = uint32(atkSlots) + uint32(k-10)*sangokushi.SlotStride
			}
			x, err := o.Long(base + 4)
			die(err)
			y, err := o.Long(base + 8)
			die(err)
			m, err := o.Byte(base + 0x10)
			die(err)
			c.FinalX[k], c.FinalY[k], c.FinalM[k] = int(int32(x)), int(int32(y)), int(m)
		}
		out = append(out, c)
	}
	return out
}

// writeRoundBoard 擺整輪要用的完整盤面，回傳雙方總兵。
func writeRoundBoard(o *oracle.Oracle, c *roundCase, acting byte) (int, int) {
	const defRuler, atkRuler = 3, 7
	has := func(list []int, v int) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	var b sangokushi.Board
	b.Ruler = acting
	for j := 0; j < cells; j++ {
		b.Terrain[j] = (c.Terrain[j] - '0') << 3
		b.Fire[j] = c.Fire[j] - '0'
	}
	b.Terrain[c.HQY*sangokushi.TerrainCols+c.HQX] |= 6
	for k := 0; k < 10; k++ {
		die(sangokushi.BattleUnit{Addr: defSlots + uint32(k)*sangokushi.SlotStride}.Write(o))
		die(sangokushi.BattleUnit{Addr: atkSlots + uint32(k)*sangokushi.SlotStride}.Write(o))
	}
	ownT, enyT := 0, 0
	for j := 0; j < cells; j++ {
		ch := c.Units[j]
		if ch == '.' {
			continue
		}
		idx, lord := 0, byte(defRuler)
		slot, gen := uint32(0), uint32(0)
		if ch >= 'a' && ch <= 'j' {
			idx = int(ch - 'a')
			slot, gen = defSlots+uint32(idx)*sangokushi.SlotStride, defGens+uint32(idx)*0x30
		} else {
			idx = 10 + int(ch-'A')
			k := idx - 10
			slot, gen, lord = atkSlots+uint32(k)*sangokushi.SlotStride, atkGens+uint32(k)*0x30, atkRuler
		}
		die(sangokushi.General{Addr: gen, Ruler: lord,
			Intelligence: byte(c.Intel[idx]), Strength: byte(c.Force[idx])}.Write(o))
		if has(c.Lords, idx) {
			die(o.SetByte(gen+0x1B, 0x02))
		}
		if has(c.Chiefs, idx) {
			die(o.SetByte(gen+0x2A, 0x80))
		}
		die(sangokushi.BattleUnit{Addr: slot, General: gen,
			X: u32(j % sangokushi.TerrainCols), Y: u32(j / sangokushi.TerrainCols),
			Troops: u32(c.Troops[idx])}.Write(o))
		die(sangokushi.SetMobility(o, slot, byte(c.Mob[idx])))
		b.Occupy[j] = slot
		// **地形位元組的 bit7 ＝ 這一格有單位**（`sub_662E2` `lsr.b #3` 之後
		// 類型被撐到 ≥ 16 而被判成不可通行）。單位格陣列 `0x77996` 是另一份，
		// 兩份都要擺，不然原版會覺得整張地圖都空著，走位時可以疊在一起。
		b.Terrain[j] |= 0x80
		if lord == acting {
			ownT += c.Troops[idx]
		} else {
			enyT += c.Troops[idx]
		}
	}
	die(b.Write(o))

	for i := uint32(0); i < 0x100; i += 4 {
		die(o.SetLong(nationBuf+i, 0))
	}
	die(o.SetLong(nationBuf+4, u32(c.Gold)))
	die(o.SetLong(nationBuf+8, u32(c.Rice)))
	die(o.SetLong(nationBuf+0x50, u32(c.AtkGold)))
	die(o.SetLong(nationBuf+0x54, u32(c.AtkRice)))
	die(o.SetLong(sangokushi.NationPtr, nationBuf))

	opp, oppSlots := uint32(atkRuler), uint32(atkSlots)
	if acting == atkRuler {
		opp, oppSlots = defRuler, defSlots
	}
	die(o.SetLong(sangokushi.DefRuler, defRuler))
	die(o.SetLong(sangokushi.AtkRuler, atkRuler))
	die(o.SetLong(sangokushi.ActingRuler, uint32(acting)))
	die(o.SetLong(sangokushi.OppRuler, opp))
	die(o.SetLong(sangokushi.OppSlots, oppSlots))
	actingSlots := uint32(defSlots)
	if acting == atkRuler {
		actingSlots = atkSlots
	}
	die(o.SetLong(sangokushi.ActingSlots, actingSlots))
	die(o.SetLong(sangokushi.HQX, u32(c.HQX)))
	die(o.SetLong(sangokushi.HQY, u32(c.HQY)))
	die(o.SetLong(sangokushi.Wind, u32(c.Wind)))
	die(o.SetLong(sangokushi.BattleDay, u32(c.Day)))
	die(o.SetLong(sangokushi.Difficulty, 1))
	die(o.SetLong(sangokushi.PolicyFlag, 0))
	die(sangokushi.ResetStepCost(o))
	die(sangokushi.BuildCastleList(o, adjBuf, scratch+0x1880))
	return ownT, enyT
}
