package x68k

import "fmt"

// iocsNames 是 IOCS 呼叫號對名稱。
//
// 來源：Data Crystal 的 X68000 IOCS 手冊整理
// （https://datacrystal.tcrf.net/wiki/X68k/IOCS）。這是**平台的公開規格**，
// 不是遊戲的逆向結果——照 `retro-hardware-spec-first` 的原則，
// 平台規格夠用時就不要把 BIOS 當遊戲來反組譯。
//
// 只收 SANMAIN.Z 會用到的那些（普查與實跑各自看到的聯集）。
// 名稱是「這個號碼叫什麼」的參考，**行為仍以實跑對照為準**。
var iocsNames = map[uint16]string{
	0x00: "_B_KEYINP",
	0x01: "_B_KEYSNS",
	0x0C: "_TVCTRL",
	0x0E: "_TGUSEMD",
	0x10: "_CRTMOD",
	0x20: "_B_PUTC",
	0x2E: "_B_CONSOL",
	0x40: "_B_SEEK",
	0x4E: "_B_DRVCHK",
	0x6C: "_VDISPST",
	0x70: "_MS_INIT",
	0x71: "_MS_CURON",
	0x73: "_MS_STAT",
	0x74: "_MS_GETDT",
	0x75: "_MS_CURGT",
	0x76: "_MS_CURST",
	0x77: "_MS_LIMIT",
	0x78: "_MS_OFFTM",
	0x79: "_MS_ONTM",
	0x72: "_MS_CUROF",
	0x7D: "_SKEY_MOD",
	0x80: "_B_INTVCS",
	0x81: "_B_SUPER",
	0x90: "_G_CLR_ON",
	0xB2: "_VPAGE",
	0xBC: "_PAINT",
	0xC0: "_SP_INIT",
	0xF0: "_OPMDRV",
}

// IOCSName 回傳呼叫號的名稱；沒收錄就回 `$xx`——不猜。
func IOCSName(n uint16) string {
	if s, ok := iocsNames[n]; ok {
		return s
	}
	return fmt.Sprintf("$%02X", n)
}
