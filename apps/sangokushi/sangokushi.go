// Package sangokushi 是第四層：KOEI 1988 X68000 版《三國志》專屬的東西。
//
// **位址常數、狀態版面、攔截點只能出現在這一層**（`docs/spec/006`）。
// 底下三層（機器、觀測、runtime）不知道跑的是哪一支程式——那是
// 「第二個 X68000 程式不必 fork」的判準。
//
// 位址的來源是 `sangokushi_x68k_cht` 的 `docs/re/`／`docs/mechanics/`，
// 每一個都標了出處；沒有出處的不放進來。
package sangokushi

import (
	"github.com/wicanr2/x68golem/internal/x68k"
	"github.com/wicanr2/x68golem/oracle"
	"github.com/wicanr2/x68golem/runtime/xc"
)

// 位址常數。`.Z` 是固定位址的平坦映像，所以 IDA 的位址就是執行期位址
// （`docs/spec/006`）。
const (
	// Rand 是 `sub_60580(n) == rand() % n`。
	// 出處：`sangokushi_x68k_cht` 的 `docs/re/12`（L0）。
	Rand = 0x60580

	// RandWrapper 是包住 FLOAT2.X `$FE0E` 的三行小函式（`FE0E ; rts`）。
	// 亂數真正的來源在那裡，不在遊戲的執行檔裡。
	RandWrapper = 0x6F28A

	// Year／Month 是遊戲狀態裡的年與月。
	// 出處：`sangokushi_x68k_cht` 的 `docs/formats/03`
	//（年 L1、月 L2，劇本 1 起始是 189 年 1 月）。
	Year  = 0x7A150
	Month = 0x7A158
)

// Date 讀出目前的年與月。
//
// **這是「跑到哪裡了」最便宜的斷言**：畫面比對要 128 KB，而
// 「189 年 1 月 → 189 年 2 月」是兩個整數。
func Date(o *oracle.Oracle) (year, month uint32, err error) {
	year, err = o.Long(Year)
	if err != nil {
		return
	}
	month, err = o.Long(Month)
	return
}

// OnRand 在每一次 `sub_60580(n)` 被呼叫時通知一次，參數是 n。
//
// 這比攔 `$FE0E` 高一層：`$FE0E` 看到的是「取一個亂數」，
// 這裡看到的是「要一個 0..n−1 的數」——**對照公式時要的是後者**。
func OnRand(o *oracle.Oracle, fn func(n uint32)) {
	o.OnCall(Rand, func(f *x68k.Frame) {
		n, err := xc.Long(f, 0)
		if err != nil {
			return
		}
		fn(n)
	})
}

// ForceRand 把 `sub_60580(n)` 整支換掉：由 fn 決定回傳值。
//
// 與「把 `rand()` 固定成一個值」不同——那個固定的是**原始亂數**，
// 取餘之後每個 n 得到的值不一樣；這個固定的是**結果**。
// 要對照 `docs/mechanics` 裡寫成 `rand(n)` 的公式時，用這個比較直接。
func ForceRand(o *oracle.Oracle, fn func(n uint32) uint32) {
	o.Intercept(Rand, func(f *x68k.Frame) (uint32, bool) {
		n, err := xc.Long(f, 0)
		if err != nil {
			return 0, false
		}
		return fn(n), true
	})
}
