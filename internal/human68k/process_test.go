package human68k

import (
	"encoding/binary"
	"testing"
)

// crt0 讀 A0+0x10（區塊位址）、A0+0x30／+0x34、A0+0x80／+0xC4（兩個字串），
// 並拿 A1 當「程式的結束」去算要保留多少記憶體。這一支釘住那個版面。
func TestProcessLayout(t *testing.T) {
	ram := make([]byte, 0x200000)
	base := uint32(0x4FFFA)
	p := &Process{
		BlockAddr:  base - ProcessBlockSize,
		DataEnd:    0x8A9B6,
		ProgramEnd: 0x8B874,
		BlockEnd:   0x1FFC00,
		Path:       "A:\\",
		Name:       "SANMAIN.Z",
		CmdLine:    "",
	}
	a0, a1, a2, a3 := p.Layout(ram)

	if a0 != 0x4FEFA {
		t.Fatalf("A0 = 0x%X，應該是載入基底 − 0x100", a0)
	}
	if a0+ProcessBlockSize != base {
		t.Fatalf("程式映像沒有接在管理區塊後面")
	}
	if a1 != 0x8B874 {
		t.Fatalf("A1 = 0x%X，應該是程式的結束而不是記憶體的結束", a1)
	}
	if got := binary.BigEndian.Uint32(ram[a0+0x08:]); got != 0x1FFC00 {
		t.Errorf("管理標頭 +0x08（區塊結束）= 0x%X", got)
	}
	if got := string(ram[a0+0x80 : a0+0x83]); got != "A:\\" {
		t.Errorf("A0+0x80 的路徑是 %q", got)
	}
	if got := string(ram[a0+0xC4 : a0+0xCD]); got != "SANMAIN.Z" {
		t.Errorf("A0+0xC4 的檔名是 %q", got)
	}
	if a2 <= a0 || a2 >= a0+0x80 {
		t.Errorf("命令列 0x%X 應該落在管理標頭與路徑之間的空隙", a2)
	}
	if ram[a2] != 0 || ram[a2+1] != 0 {
		t.Errorf("空命令列的長度 byte 與結尾都該是 0")
	}
	// 環境區塊要在管理區塊外面，而且開頭那個 long 是**配置的總長度**。
	// crt0 把它加進堆積游標（`docs/findings/019`）——給 0 的話堆積整批
	// 往下移 0x200 bytes，之後每一份載入的資料都落在錯的位址。
	if a3+DefaultEnvSize != a0 {
		t.Errorf("環境 0x%X 應該剛好接在管理區塊下面", a3)
	}
	if got := binary.BigEndian.Uint32(ram[a3:]); got != DefaultEnvSize {
		t.Errorf("環境區塊開頭的長度 = 0x%X，應該是 0x%X", got, DefaultEnvSize)
	}
	if ram[a3+4] != 0 {
		t.Errorf("空環境的第一個字串位元組應該是 0")
	}
}
