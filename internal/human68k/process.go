package human68k

import "encoding/binary"

// Human68k 交棒給 `.Z` 程式時的契約。
//
// 這一份是從 SANMAIN.Z 的 crt0 反推的（`docs/findings/002`），不是抄手冊：
// 程式讀了哪幾個欄位，我們就知道哪幾個欄位一定要有值。
//
//	0x06E6DC  move.l a2,($8A9F6).l     ← A2 存起來（後面當字串逐 byte 讀）
//	0x06E6E2  move.l a3,($8A9EA).l     ← A3 存起來
//	0x06E6EC  lea ($100,a0),a5         ← A0+0x100
//	0x06E6F6  move.l ($34,a0),d0       ← A0+0x34
//	0x06E706  move.l ($30,a0),d0       ← A0+0x30
//	0x06E716  move.l a1,($8A9CE).l     ← A1 存起來
//	0x06E71C  lea ($10,a0),a5          ← A0+0x10，稍後當 _SETBLOCK 的區塊位址
//	0x06E9EE  lea ($80,a0),a0          ← A0+0x80，null 結尾字串
//	0x06E9FA  lea ($C4,a0),a0          ← A0+0xC4，null 結尾字串
//
// 兩件事因此可以確定（L2）：
//
//   - **A0 指向記憶體管理標頭**，程式的區塊從 A0+0x10 開始
//     （`_SETBLOCK` 拿的就是那個位址），而程式映像本身在 A0+0x100。
//     所以 A0 = 載入基底 − 0x100。
//   - A0+0x80 與 A0+0xC4 各是一個 null 結尾字串（執行檔路徑與檔名）。
//
// 還沒有證據的（L3，先給自洽的值，錯了會在執行期露出來）：
// A0+0x30、A0+0x34 這兩個 long 的意義。
const (
	// ProcessBlockSize 是管理標頭 ＋ 程式管理區塊的總長度，程式映像接在後面。
	ProcessBlockSize = 0x100

	// 管理標頭（A0 起算）
	pbPrev   = 0x00 // 前一個記憶體管理指標
	pbParent = 0x04 // 管理這一塊的程序
	pbEnd    = 0x08 // 這一塊的結束位址
	pbNext   = 0x0C // 下一個記憶體管理指標

	// 程式管理區塊（A0 起算）
	// +0x30／+0x34／+0x38 是**量出來的**（`docs/findings/018`）：
	// 在 MAME 上把真機的管理區塊整塊印出來，對照 `docs/re/01` 的段界，
	//
	//	+0x30 = +0x34 = 0x0008A9B6 ＝ **data 段結束（bss 起點）**
	//	+0x38 =         0x0008B874 ＝ **bss 結束**
	//
	// 先前這兩個欄位標 L3、兩個都填了「區塊結束位址」，堆積因此整批位移
	// 168 bytes，畫地圖的迴圈讀到界外的 0（`docs/findings/017`）。
	pbDataEnd  = 0x30 // data 段結束（＝ bss 起點）
	pbDataEnd2 = 0x34 // 同上，crt0 兩個都讀
	pbBSSEnd   = 0x38 // bss 結束
	pbPath     = 0x80 // 執行檔路徑（null 結尾）
	pbName     = 0xC4 // 執行檔檔名（null 結尾）
)

// DefaultEnvSize 是 Human68k 交給程式的環境區塊長度。
//
// 環境區塊的格式是「long 總長度 ＋ 一串 null 結尾的 `NAME=VALUE` ＋ 一個 null」，
// **那個 long 是配置的總長度，不是已用的長度**。COMMAND.X 預設配 512 bytes，
// 在 MAME 上量到的真機值就是 `0x200`（`docs/findings/019`）。
//
// 這個值會直接進到程式的堆積起點：crt0 讀環境區塊的第一個 long，
// 加 5、向下對齊到偶數之後累加進配置器的游標（`0x06E762`–`0x06E774`）。
// 給 0 的話堆積整批往下移 0x200 bytes，之後每一份載入的資料都落在錯的位址。
const DefaultEnvSize = 0x200

// Process 描述要交給程式的那一組東西。
type Process struct {
	BlockAddr uint32 // ＝ A0，等於載入基底 − ProcessBlockSize
	// DataEnd 是 data 段的結束（＝ bss 起點），放進管理區塊的 +0x30／+0x34。
	DataEnd uint32
	// ProgramEnd ＝ A1：**程式映像的結束**（bss 之後），不是記憶體的結束。
	//
	// 這一點是量出來的：crt0 以 A1 為起點，逐段加上對齊過的堆積、堆疊與
	// 環境複本的大小，最後 `sub.l a5,d1` 得到要保留的長度交給 _SETBLOCK。
	// 一開始把 A1 給成「記憶體結束」，算出來的長度是 0x220D08——
	// 比整台機器的記憶體還大，當場就知道給錯了（`docs/findings/002`）。
	ProgramEnd uint32
	// BlockEnd ＝ 管理標頭 +0x08：這一塊記憶體的結束。
	BlockEnd uint32
	Path     string
	Name     string
	CmdLine  string
	// EnvAddr 是環境區塊的位址（＝ A3）。0 表示放在管理區塊下面。
	EnvAddr uint32
	// EnvSize 是環境區塊的總長度，會寫進區塊開頭的 long。
	// 0 表示用 DefaultEnvSize。
	EnvSize uint32
}

// Layout 把 Process 寫進記憶體，回傳進入時的 A0–A3。
//
// 命令列與環境放在管理區塊後面的空隙裡（A0+0x10 到 A0+0x80 之間是空的），
// 這樣不必另外配置。
func (p *Process) Layout(ram []byte) (a0, a1, a2, a3 uint32) {
	put32 := func(off uint32, v uint32) {
		binary.BigEndian.PutUint32(ram[p.BlockAddr+off:], v)
	}
	putStr := func(off uint32, s string) uint32 {
		copy(ram[p.BlockAddr+off:], s)
		ram[p.BlockAddr+off+uint32(len(s))] = 0
		return p.BlockAddr + off
	}
	put32(pbPrev, 0)
	put32(pbParent, 0)
	put32(pbEnd, p.BlockEnd)
	put32(pbNext, 0)
	put32(pbDataEnd, p.DataEnd)
	put32(pbDataEnd2, p.DataEnd)
	put32(pbBSSEnd, p.ProgramEnd)
	putStr(pbPath, p.Path)
	putStr(pbName, p.Name)

	// 命令列：Human68k 的格式是「長度 byte ＋ 內容 ＋ 0」。
	cmd := p.BlockAddr + 0x18
	ram[cmd] = byte(len(p.CmdLine))
	copy(ram[cmd+1:], p.CmdLine)
	ram[cmd+1+uint32(len(p.CmdLine))] = 0

	// 環境區塊要有真正的 512 bytes：crt0 把開頭那個 long 當成要跳過的長度，
	// 而它自己的堆積游標就是從那裡往上長的。
	size := p.EnvSize
	if size == 0 {
		size = DefaultEnvSize
	}
	env := p.EnvAddr
	if env == 0 {
		env = p.BlockAddr - size
	}
	for i := uint32(0); i < size; i++ {
		ram[env+i] = 0
	}
	binary.BigEndian.PutUint32(ram[env:], size)

	return p.BlockAddr, p.ProgramEnd, cmd, env
}
