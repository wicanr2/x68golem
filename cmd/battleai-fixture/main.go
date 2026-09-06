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
	"github.com/wicanr2/x68golem/oracle"
)

// 合成盤面的位址：主記憶體 2 MB，遊戲的堆積到 0xFCA86 為止，樁在 0x1F0000 起。
// 0x1E0000 這一帶誰都不會碰。
const (
	scratch  = 0x1E0000
	fieldBuf = scratch + 0x0000 // 672 bytes
	unitBase = scratch + 0x0400 // 每個單位槽 0x40
	genBase  = scratch + 0x0C00 // 每筆武將記錄 0x40
	cells    = sangokushi.BattleRows * sangokushi.TerrainCols
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
}

func main() {
	z := flag.String("z", "", "SANMAIN.Z（必填，玩家自備）")
	out := flag.String("o", "battleai.json", "輸出的 fixture")
	boards := flag.Int("boards", 24, "隨機盤面數")
	seed := flag.Int64("seed", 1, "亂數種子（固定才能重現）")
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

	b, err := json.MarshalIndent(f, "", " ")
	die(err)
	die(os.WriteFile(*out, append(b, '\n'), 0o644))
	fmt.Printf("寫出 %s：六角距離 %d、方向碼 %d、補給 %d、突撃 %d、距離場 %d 個盤面\n",
		*out, len(f.HexDistance), len(f.DirCode), len(f.Supply),
		len(f.ChargeOdds), len(f.DistField))
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
