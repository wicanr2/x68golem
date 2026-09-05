package human68k

import "fmt"

// dosCallNames 只收**有證據的**呼叫號。
//
// 來源兩份：
//
//  1. SANMAIN.Z 的 F-line 站點普查（`sangokushi_x68k_cht` 的
//     `workplace/ida/x68k/census/doscalls.txt`，28 個站點、11 種呼叫號），
//     那份清單同時給了號碼與名稱。
//  2. Data Crystal 的 Human68k DOS call 手冊整理
//     （https://datacrystal.tcrf.net/wiki/X68k/DOSCALL）——**平台的公開規格**，
//     補上普查裡沒有、但實跑會碰到的號碼（目前是 `$21 _FNCKEY`）。
//
// **不補沒查過的號碼。** Human68k 的完整 DOS call 表不難找，但這個表
// 是拿來標記「我們知道這是什麼」的，塞進沒驗過的名字只會讓報告看起來
// 比實際知道的多。沒收錄的號碼在報告裡就印 `$FFxx`。
var dosCallNames = map[uint16]string{
	0x06: "_INPOUT",
	0x1A: "_GETSS",
	0x21: "_FNCKEY",
	0x20: "_SUPER",
	0x23: "_CONCTRL",
	0x3C: "_CREATE",
	0x3D: "_OPEN",
	0x3E: "_CLOSE",
	0x3F: "_READ",
	0x40: "_WRITE",
	0x42: "_SEEK",
	0x44: "_IOCTRL",
}

// DOSCallName 把 F-line 指令字翻成名稱。
//
// F-line（`$Fxxx`）在 Human68k 上不是只有 DOS call：
//
//	$FF00–$FF7F  DOS call（Human68k 本體）
//	$FE00–$FEFF  FLOAT2.X／FLOAT4.X 的浮點與長整數運算
//	其他         沒有人接就是非法指令
//
// **這個遊戲兩種都會用到。** Disk A 的 `CONFIG.SYS` 明寫
// `DEVICE = \SYS\FLOAT2.X`，所以 `$FExx` 在真機上有人接
// （`docs/findings/004`）。沒收錄的號碼一律照原樣印，不猜。
func DOSCallName(opcode uint16) string {
	if IsDOSCall(opcode) {
		if name, ok := dosCallNames[opcode&0x00FF]; ok {
			return name
		}
		return fmt.Sprintf("$%04X", opcode)
	}
	if IsFloatCall(opcode) {
		return fmt.Sprintf("FLOAT2 $%02X", opcode&0x00FF)
	}
	return fmt.Sprintf("$%04X（沒有人接的 F-line）", opcode)
}

// IsDOSCall 判斷一個指令字是不是 Human68k 的 DOS call。
func IsDOSCall(opcode uint16) bool { return opcode >= 0xFF00 && opcode <= 0xFF7F }

// IsFloatCall 判斷一個指令字是不是 FLOAT2.X 那一段。
func IsFloatCall(opcode uint16) bool { return opcode >= 0xFE00 && opcode <= 0xFEFF }
