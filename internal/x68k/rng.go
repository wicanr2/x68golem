package x68k

import "fmt"

// 亂數控制——**這是整個專案存在的主要理由**（docs/spec/005 §4）。
//
// 對拍時「原版跟 remake 為什麼不一樣」只有兩個來源：規則不同，或亂數不同。
// 亂數不受控的時候這兩個分不開，只能多跑幾場再談信賴區間；把亂數變成輸入，
// 對拍就從統計推論變成逐項比對。
//
// ## 掛在哪裡
//
// 《三國志》的亂數**不在 `SANMAIN.Z` 裡**。`sub_60580(n)` 只是包裝：
//
//	0x60580  move.l d2,-(sp)
//	0x60582  move.l (8,sp),d2        ; d2 = n
//	0x60586  tst.l  d2
//	0x60588  bne.s  0x6058E
//	0x6058A  moveq  #0,d0            ; n == 0 → 回 0
//	0x6058C  bra.s  0x6059C
//	0x6058E  jsr    $6F28A           ; → FE0E ; rts       ＝ rand()
//	0x60594  move.l d2,d1
//	0x60596  jsr    $6ED4A           ; 長整數取餘
//	0x6059C  move.l (sp)+,d2
//	0x6059E  rts
//
// 也就是 `sub_60580(n) == rand() % n`，而 `rand()` 是 **`FLOAT2.X`** 提供的
// F-line 服務 `$FE0E`（`CONFIG.SYS` 載的驅動，`docs/findings/005`）。
//
// 掛在 `$FE0E` 比掛在遊戲的位址好：那是**機器層的服務邊界**，
// 換一個 X68000 程式一樣成立。
type RNGMode int

const (
	// RNGUnset：還沒指定來源。**碰到就停**——見下方「為什麼沒有直通模式」。
	RNGUnset RNGMode = iota
	// RNGFixed：每次都回同一個值。把亂數從方程式裡消掉，只剩規則。
	RNGFixed
	// RNGSeq：依序回指定的值，用完就報錯。精確構造情境。
	RNGSeq
	// RNGReplay：回放一條錄下來的流。同一條流、不同盤面，就是交叉檢定。
	RNGReplay
)

// RNG 是受控的亂數來源。
//
// ## 為什麼沒有「直通」模式
//
// 直通要能重現 `FLOAT2.X` 的 `rand()` **逐位元相同**，而那個演算法目前
// 還沒解出來（它在磁碟上的 `FLOAT2.X` 裡，不在遊戲的執行檔裡）。
// 在解出來之前，**任何「大概對」的亂數都比停下來糟**：它會讓對拍產生
// 自洽但錯的結論，而那正是這個專案要消滅的東西。
//
// 所以預設是 `RNGUnset`，碰到 `rand()` 就 fail-closed。
type RNG struct {
	Mode  RNGMode
	Value uint32   // RNGFixed
	Seq   []uint32 // RNGSeq／RNGReplay
	pos   int

	// Log 記下每一次取值：對拍要「同一條流」時，先錄一次再回放。
	Log []uint32
	// Seeds 記下每一次 srand 的種子。
	Seeds []uint32
}

// Next 取一個亂數。
func (r *RNG) Next() (uint32, error) {
	var v uint32
	switch r.Mode {
	case RNGFixed:
		v = r.Value
	case RNGSeq, RNGReplay:
		if r.pos >= len(r.Seq) {
			return 0, fmt.Errorf("亂數序列用完了（已經取了 %d 個）——"+
				"**不會退回真亂數**，因為安靜換來源會讓「沒對到」看起來像「對到了」", r.pos)
		}
		v = r.Seq[r.pos]
		r.pos++
	default:
		return 0, fmt.Errorf("rand() 被呼叫了，但還沒指定亂數來源。"+
			"FLOAT2.X 的 rand() 演算法還沒解出來，所以沒有直通模式——"+
			"用 Fixed／Seq／Replay 指定一個受控的來源")
	}
	r.Log = append(r.Log, v)
	return v, nil
}

// Seed 記下一次 srand。受控模式下種子不影響取值——**這是刻意的**：
// 序列由呼叫端決定，不由遊戲決定。
func (r *RNG) Seed(v uint32) { r.Seeds = append(r.Seeds, v) }

// FLOAT2 的服務號碼。目前只認得這兩個，其餘 fail-closed。
const (
	floatSrand = 0x0D
	floatRand  = 0x0E
)

// InstallFloat 登記 FLOAT2.X 那一段裡我們認得的服務。
func (m *Machine) InstallFloat() {
	if m.RNG == nil {
		m.RNG = &RNG{}
	}
	if m.FloatCalls == nil {
		m.FloatCalls = map[uint16]func(*Machine) error{}
	}
	m.FloatCalls[floatSrand] = func(mm *Machine) error {
		mm.RNG.Seed(mm.CPU.State.D[0])
		mm.SetResult(0)
		return nil
	}
	m.FloatCalls[floatRand] = func(mm *Machine) error {
		v, err := mm.RNG.Next()
		if err != nil {
			return err
		}
		mm.SetResult(v)
		return nil
	}
}
