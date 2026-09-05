package human68k

import (
	"encoding/binary"
	"os"
	"testing"
)

// buildZ 造一份合法的 `.Z`。**不用原版檔案**——本儲存庫不含原版素材，
// 而格式的正確性本來就該由自己造得出來的樣本證明。
func buildZ(base, textSize, dataSize, bssSize uint32, body []byte) []byte {
	h := make([]byte, ZHeaderSize)
	binary.BigEndian.PutUint16(h[0:], 0x601A)
	binary.BigEndian.PutUint32(h[0x02:], textSize)
	binary.BigEndian.PutUint32(h[0x06:], dataSize)
	binary.BigEndian.PutUint32(h[0x0A:], bssSize)
	binary.BigEndian.PutUint32(h[0x16:], base)
	binary.BigEndian.PutUint16(h[0x1A:], 0xFFFF)
	return append(h, body...)
}

func TestParseZ(t *testing.T) {
	body := make([]byte, 8)
	im, err := ParseZ(buildZ(0x4FFFA, 4, 4, 16, body))
	if err != nil {
		t.Fatal(err)
	}
	if im.Base != 0x4FFFA || im.Entry != 0x4FFFA {
		t.Fatalf("基底／進入點 = 0x%X／0x%X", im.Base, im.Entry)
	}
	if im.TextEnd() != 0x4FFFE || im.DataEnd() != 0x50002 || im.BSSEnd() != 0x50012 {
		t.Fatalf("段界 0x%X 0x%X 0x%X", im.TextEnd(), im.DataEnd(), im.BSSEnd())
	}
}

func TestParseZRejects(t *testing.T) {
	body := make([]byte, 8)
	good := buildZ(0x4FFFA, 4, 4, 0, body)

	bad := append([]byte(nil), good...)
	bad[0] = 0
	if _, err := ParseZ(bad); err == nil {
		t.Error("識別子壞了應該要失敗")
	}

	bad = append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[0x02:], 999) // text 長度與檔案大小對不上
	if _, err := ParseZ(bad); err == nil {
		t.Error("長度對不上應該要失敗")
	}

	bad = append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[0x16:], 0x4FFFB) // 奇數基底
	if _, err := ParseZ(bad); err == nil {
		t.Error("奇數基底應該要失敗")
	}
}

// TestParseZRealFile 只有在使用者自己指了原版檔案時才跑。
// 缺檔就 skip——不做代用品（CLAUDE.md 的硬規則）。
func TestParseZRealFile(t *testing.T) {
	path := os.Getenv("X68GOLEM_TEST_Z")
	if path == "" {
		t.Skip("X68GOLEM_TEST_Z 沒設，跳過原版檔案的檢查")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("讀不到 %s：%v", path, err)
	}
	im, err := ParseZ(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("基底 0x%X  text %d  data %d  bss %d  bss 結束 0x%X",
		im.Base, im.TextSize, im.DataSize, im.BSSSize, im.BSSEnd())
}
