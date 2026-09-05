package x68k

// CRTC 目前只做一件事：**高速クリア／ラスタコピー 的動作位元**。
//
// 為什麼是這一位先做：SANMAIN.Z 開機途中有一段
//
//	0x070D20  lea    ($E80481).l,a0
//	0x070D26  ori.b  #2,(a0)      ← 要求動作
//	0x070D2A  btst   #1,(a0)
//	0x070D2E  beq.s  -6           ← 等它變成 1（硬體收到了）
//	0x070D30  btst   #1,(a0)
//	0x070D34  bne.s  -6           ← 再等它變回 0（硬體做完了）
//
// 兩個方向都等，所以這一位既不能永遠是 0（第一個迴圈卡死），
// 也不能永遠是 1（第二個迴圈卡死）。**它必須會自己清掉。**
//
// 推論等級：
//
//   - L2：這一位由程式設起、由硬體清掉。上面那兩個迴圈就是證據。
//   - L3：**多久之後清掉**。真機是在下一次垂直歸線完成，我們現在用
//     「一格畫面的週期數」近似。MAME 上以 60 Hz 取樣 5045 次，
//     $E80481 每次都讀到 0x00——與「平常是 0，只有動作進行中才是 1」相容，
//     但沒有告訴我們那段時間有多長。
//
// 真的要做對，要等 M3 把 CRTC 的時序接上去。在那之前這裡是**明講的近似**，
// 不是「看起來會動就好」。
type CRTC struct {
	// FrameCycles 是一格畫面的 CPU 週期數。
	// X68000 的 CPU 是 10 MHz，垂直頻率約 55.46 Hz（標準 256 色模式）。
	FrameCycles uint64

	op        byte   // $E80481 的內容
	clearAt   uint64 // 動作位元要在這個週期數之後清掉
	pending   bool
}

const crtcOpPort = 0xE80481

// NewCRTC 用預設的一格畫面週期數建一個 CRTC。
func NewCRTC() *CRTC {
	return &CRTC{FrameCycles: 10_000_000 / 55} // ≈ 181,818
}

// Read 讀動作埠。
func (c *CRTC) Read(cycles uint64) byte {
	if c.pending && cycles >= c.clearAt {
		c.pending = false
		c.op &^= 0x0E // 高速クリア／ラスタコピー 的請求位元一起清掉
	}
	return c.op
}

// Write 寫動作埠。設起任何一個動作位元就開始計時。
func (c *CRTC) Write(cycles uint64, v byte) {
	c.op = v
	if v&0x0E != 0 {
		c.pending = true
		c.clearAt = cycles + c.FrameCycles
	}
}
