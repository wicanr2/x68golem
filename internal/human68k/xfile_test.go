package human68k

import (
	"encoding/binary"
	"os"
	"testing"
)

func buildX(base, entry uint32, text []byte, reloc []byte) []byte {
	h := make([]byte, XHeaderSize)
	binary.BigEndian.PutUint16(h[0:], 0x4855)
	binary.BigEndian.PutUint32(h[0x04:], base)
	binary.BigEndian.PutUint32(h[0x08:], entry)
	binary.BigEndian.PutUint32(h[0x0C:], uint32(len(text)))
	binary.BigEndian.PutUint32(h[0x18:], uint32(len(reloc)))
	out := append(h, text...)
	return append(out, reloc...)
}

func TestParseX(t *testing.T) {
	text := make([]byte, 16)
	binary.BigEndian.PutUint32(text[8:], 0x1000) // 一個要重定位的長字
	// 重定位表：位移 8，補一個 long；然後 0 結束。
	reloc := []byte{0x00, 0x08, 0x00, 0x00}
	x, err := ParseX(buildX(0, 0x10, text, reloc))
	if err != nil {
		t.Fatal(err)
	}
	if x.Entry != 0x10 || x.TextSize != 16 {
		t.Fatalf("進入點 0x%X、text %d", x.Entry, x.TextSize)
	}
	mem := append([]byte(nil), x.Body...)
	n, err := x.Relocate(mem, 0x20000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("套了 %d 個重定位", n)
	}
	if got := binary.BigEndian.Uint32(mem[8:]); got != 0x21000 {
		t.Fatalf("重定位之後是 0x%X，應該是 0x21000", got)
	}
}

func TestParseXRejectsBadLength(t *testing.T) {
	bad := buildX(0, 0x10, make([]byte, 16), nil)
	bad = append(bad, 0) // 多一個 byte
	if _, err := ParseX(bad); err == nil {
		t.Error("長度對不上應該要失敗")
	}
}

// 有原版檔案才跑。缺檔就 skip。
func TestParseXRealFile(t *testing.T) {
	path := os.Getenv("X68GOLEM_TEST_X")
	if path == "" {
		t.Skip("X68GOLEM_TEST_X 沒設")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("讀不到 %s：%v", path, err)
	}
	x, err := ParseX(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("基底 0x%X 進入點 0x%X text %d data %d bss %d reloc %d",
		x.Base, x.Entry, x.TextSize, x.DataSize, x.BSSSize, len(x.Reloc))
	mem := append([]byte(nil), x.Body...)
	n, err := x.Relocate(mem, 0x30000)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("套了 %d 個重定位", n)
	if n == 0 {
		t.Error("一個重定位都沒套，重定位表的解讀可能是錯的")
	}
}
