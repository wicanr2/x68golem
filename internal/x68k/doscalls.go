package x68k

import "fmt"

// Human68k 的 DOS call 用**堆疊傳參數**，呼叫端自己收拾
// （`$FF4A` 後面緊接著 `addq.l #8,sp`），回傳值放 D0。
//
// 服務執行的當下我們已經把例外堆疊框收掉了，所以 SP 就是呼叫端推參數
// 之前的那個 SP——第一個參數在 SP+0。

// SP 回傳目前有效的堆疊指標。user mode 用 USP，supervisor mode 用 SSP。
func (m *Machine) SP() uint32 {
	if m.CPU.State.SR&0x2000 != 0 {
		return m.CPU.State.SSP
	}
	return m.CPU.State.USP
}

// ArgWord 讀堆疊上偏移 off 處的 word 參數。
func (m *Machine) ArgWord(off uint32) (uint16, error) {
	return m.Bus.ReadWord(m.SP()+off, 5)
}

// ArgLongAt 讀堆疊上偏移 off 處的 long 參數。
func (m *Machine) ArgLongAt(off uint32) (uint32, error) {
	hi, err := m.Bus.ReadWord(m.SP()+off, 5)
	if err != nil {
		return 0, err
	}
	lo, err := m.Bus.ReadWord(m.SP()+off+2, 5)
	if err != nil {
		return 0, err
	}
	return uint32(hi)<<16 | uint32(lo), nil
}

// ArgLong 讀第 n 個 long 參數（從 0 起算）。
func (m *Machine) ArgLong(n int) (uint32, error) {
	addr := m.SP() + uint32(n*4)
	hi, err := m.Bus.ReadWord(addr, 5)
	if err != nil {
		return 0, err
	}
	lo, err := m.Bus.ReadWord(addr+2, 5)
	if err != nil {
		return 0, err
	}
	return uint32(hi)<<16 | uint32(lo), nil
}

// SetResult 把回傳值放進 D0。
func (m *Machine) SetResult(v uint32) { m.CPU.State.D[0] = v }

// InstallDOSCalls 登記目前實作好的 DOS call。
func (m *Machine) InstallDOSCalls() {
	m.DOSCalls[0x25] = dosIntvcs
	m.installDriverCalls()
	m.DOSCalls[0x09] = dosPrint
	m.DOSCalls[0x20] = dosSuper
	m.DOSCalls[0x21] = dosFnckey
	m.DOSCalls[0x44] = dosIoctrl
	m.DOSCalls[0x4A] = dosSetblock
}

// dosIntvcs 是 `$FF25 _INTVCS(向量編號 word, 新位址 long)`：換掉一個
// 中斷／例外向量，回傳**換掉之前**的位址。
//
// 參數形狀是量出來的（L2）：
//
//	0x06E8FC  pea    (d16,pc)        ← 先推 long
//	0x06E902  move.w #$FFF1,-(sp)    ← 再推 word
//	0x06E906  $FF25
//	0x06E908  addq.l #6,sp           ← 呼叫端收 6 bytes ＝ 2 + 4
//
// 所以堆疊上是「word 在前、long 在後」。編號 0xFFxx 那一段是 Human68k
// 自己的向量，不是 68000 的例外向量表。
//
// 我們只記在一張表上，還沒有任何東西會去觸發它們（L3：回傳的舊位址
// 一律是 0，真機上會是 Human68k 自己的處理常式位址）。
func dosIntvcs(m *Machine) error {
	num, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	addr, err := m.ArgLongAt(2)
	if err != nil {
		return err
	}
	if m.Vectors == nil {
		m.Vectors = map[uint16]uint32{}
	}
	old := m.Vectors[num]
	m.Vectors[num] = addr
	m.SetResult(old)
	return nil
}

// dosPrint 是 `$FF09 _PRINT(字串.l)`：印一個字串到主控台。
//
// **結尾是 NUL，不是 MS-DOS 那個 `$`。** 一開始照 MS-DOS 的習慣找 `$`，
// 結果把 `FLOAT2.X` 招牌後面的整段程式碼都當成字串印出來了——
// 那一段輸出本身就是證據（`docs/findings/014`）。
//
// 驅動裝好之後會印一行招牌，所以跑 `FLOAT2.X` 需要它。
// 印出來的東西進 Console 的緩衝區——**看得到驅動說了什麼**，
// 那是「它真的裝起來了嗎」最直接的證據。
func dosPrint(m *Machine) error {
	ptr, err := m.ArgLongAt(0)
	if err != nil {
		return err
	}
	for i := uint32(0); i < 4096; i++ {
		b, err := m.Bus.ReadByte(ptr+i, 5)
		if err != nil {
			return err
		}
		if b == 0 {
			break
		}
		m.Console.putc(b)
	}
	m.SetResult(0)
	return nil
}

// dosSuper 是 `$FF20 _SUPER(ssp.l)`：與 IOCS 的 `$81 _B_SUPER` 是同一件事，
// 只是參數走堆疊而不是 A1。語意與坑都一樣，所以直接共用實作
// ——**一條規則只留一份實作**。
func dosSuper(m *Machine) error {
	arg, err := m.ArgLongAt(0)
	if err != nil {
		return err
	}
	saved := m.CPU.State.A[1]
	m.CPU.State.A[1] = arg
	err = iocsSuper(m)
	m.CPU.State.A[1] = saved
	return err
}

// dosFnckey 是 `$FF21 _FNCKEY(模式.w, 緩衝區.l)`：讀寫功能鍵的定義字串。
//
//	0x06E94C  pea    ($8B1A0).l    ← 先推 long 緩衝區
//	0x06E952  move.w #0,-(sp)      ← 再推 word 模式
//	0x06E956  $FF21
//	0x06E958  addq.l #6,sp
//
// 模式 0 是「把整份定義讀出來」。我們沒有功能鍵定義，所以把緩衝區清成 0
// ——**這不是敷衍**：一台剛開機、沒有人設過功能鍵的機器，讀出來就是空的。
// 設定（模式 ≥ 1）我們收下但不留，因為沒有東西會去顯示功能鍵列。
//
// L3：整份定義的長度。Human68k 的表是固定大小，我們清 712 bytes；
// 程式若只讀前面幾筆就不會發現差別。
func dosFnckey(m *Machine) error {
	mode, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	buf, err := m.ArgLongAt(2)
	if err != nil {
		return err
	}
	if mode == 0 {
		const fnckeyTableSize = 712
		for i := uint32(0); i < fnckeyTableSize; i++ {
			if err := m.Bus.WriteByte(buf+i, 0, 5); err != nil {
				return err
			}
		}
	}
	m.SetResult(0)
	return nil
}

// dosIoctrl 是 `$FF44 _IOCTRL`：對裝置驅動程式直接下指令。
//
// crt0 只用模式 0（取得裝置資訊）：
//
//	0x06E858  clr.l  d1
//	0x06E85A  move.l d1,-(sp)      ← 高字＝模式 0，低字＝檔案代號
//	0x06E85E  $FF44
//	0x06E860  addq.l #4,sp         ← 收 4 bytes ＝ word 模式 + word 代號
//
// 推 4 bytes、收 4 bytes，配上 `_IOCTRL(模式.w, 檔案代號.w)` 的形狀
// （Data Crystal 的 DOSCALL 手冊）——所以模式在 SP+0，代號在 SP+2。
//
// L3：**回傳的裝置屬性值**。我們對代號 0–4 回「字元裝置」（bit 7），
// 其餘回 0（磁碟檔）。真機上還有更多位元（重新導向、CON/NUL 等），
// 這個遊戲用不用得到還不知道。
func dosIoctrl(m *Machine) error {
	mode, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	fileno, err := m.ArgWord(2)
	if err != nil {
		return err
	}
	if mode != 0 {
		return fmt.Errorf("_IOCTRL：模式 %d 還沒實作（檔案代號 %d）", mode, fileno)
	}
	if fileno <= 4 {
		m.SetResult(0x80)
	} else {
		m.SetResult(0)
	}
	return nil
}

// dosSetblock 是 `$FF4A _SETBLOCK(區塊位址, 新長度)`：把已經配到的記憶體
// 區塊改成指定長度。crt0 用它把多要的還回去。
//
// 名稱與參數形狀是從 crt0 的行為推的（L2，`docs/findings/001`）：
// 兩個 long 推堆疊，長度由「結束 − 起點」算出來。
//
// 我們的記憶體是一整塊平的，沒有配置器，所以這裡只做三件事：
// 確認位址是我們發出去的那一塊、確認新長度放得下、記下新的結束位址。
// **放不下就回錯誤碼，不是回成功**——Human68k 失敗時回負值。
func dosSetblock(m *Machine) error {
	addr, err := m.ArgLong(0)
	if err != nil {
		return err
	}
	length, err := m.ArgLong(1)
	if err != nil {
		return err
	}
	want := m.Process.BlockAddr + 0x10
	if addr != want {
		return fmt.Errorf("_SETBLOCK：區塊位址 0x%06X 不是我們發出去的 0x%06X", addr, want)
	}
	end := uint64(addr) + uint64(length)
	if end > uint64(len(m.Bus.RAM)) {
		m.SetResult(0xFFFFFFF8) // Human68k 的「記憶體不足」是負值
		return nil
	}
	m.Process.BlockEnd = uint32(end)
	m.SetResult(0)
	return nil
}
