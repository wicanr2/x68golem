package human68k

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

// buildFAT 造一份最小的 Human68k FAT12（1024 bytes／磁區、1 磁區／叢集），
// 裡面放一個檔案。**不用原版磁碟**——格式的正確性該由自己造得出來的樣本證明。
func buildFAT(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	const bps, spc, rsv, nfat, nroot, spf = 1024, 1, 1, 2, 192, 2
	rootOff := (rsv + nfat*spf) * bps
	dataOff := rootOff + nroot*32
	img := make([]byte, dataOff+16*bps)

	binary.LittleEndian.PutUint16(img[11:], bps)
	img[13] = spc
	binary.LittleEndian.PutUint16(img[14:], rsv)
	img[16] = nfat
	binary.LittleEndian.PutUint16(img[17:], nroot)
	binary.LittleEndian.PutUint16(img[22:], spf)

	// 檔案佔兩個叢集（2、3），鏈在 3 結束。
	setFAT := func(clus int, v uint16) {
		off := rsv*bps + clus*3/2
		cur := uint16(img[off]) | uint16(img[off+1])<<8
		if clus%2 == 0 {
			cur = cur&0xF000 | v&0x0FFF
		} else {
			cur = cur&0x000F | v<<4
		}
		img[off] = byte(cur)
		img[off+1] = byte(cur >> 8)
	}
	setFAT(2, 3)
	setFAT(3, 0xFFF)

	e := img[rootOff : rootOff+32]
	base, ext, _ := strings.Cut(name, ".")
	copy(e[0:8], "        ")
	copy(e[8:11], "   ")
	copy(e[0:], base)
	copy(e[8:], ext)
	binary.LittleEndian.PutUint16(e[26:], 2)
	binary.LittleEndian.PutUint32(e[28:], uint32(len(content)))
	copy(img[dataOff:], content)
	return img
}

func TestFATFindAndRead(t *testing.T) {
	content := make([]byte, 1500) // 跨兩個叢集
	for i := range content {
		content[i] = byte(i)
	}
	fs, err := NewFAT(buildFAT(t, "TEST.DAT", content))
	if err != nil {
		t.Fatal(err)
	}
	if fs.Fallback {
		t.Error("BPB 是合法的，不該退回預設值")
	}
	e, ok := fs.Find("a:TEST.DAT")
	if !ok {
		t.Fatal("找不到檔案（磁碟機前綴應該要被忽略）")
	}
	if e.Size != uint32(len(content)) {
		t.Fatalf("大小 %d", e.Size)
	}
	got, err := fs.Read(e)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatal("讀回來的內容不一樣（跨叢集的鏈接錯了）")
	}
	if _, ok := fs.Find("NOPE.DAT"); ok {
		t.Error("不存在的檔名不該找得到")
	}
}

// BPB 壞掉時要退回 2HD 的標準參數，而不是照著壞值算。
// 已知《三國志》Disk A 就是這種情況。
func TestFATFallbackOnBadBPB(t *testing.T) {
	img := buildFAT(t, "TEST.DAT", []byte("hi"))
	binary.LittleEndian.PutUint16(img[11:], 0xFFFF) // 每磁區 bytes 是垃圾
	fs, err := NewFAT(img)
	if err != nil {
		t.Fatal(err)
	}
	if !fs.Fallback {
		t.Fatal("壞掉的 BPB 應該要觸發退回")
	}
	if fs.BytesPerSector != 1024 {
		t.Fatalf("退回之後每磁區 %d bytes", fs.BytesPerSector)
	}
	if _, ok := fs.Find("TEST.DAT"); !ok {
		t.Error("退回之後應該還是找得到檔案")
	}
}

// 有原版磁碟才跑。缺檔就 skip——不做代用品。
func TestFATRealDisk(t *testing.T) {
	path := os.Getenv("X68GOLEM_TEST_DISK")
	if path == "" {
		t.Skip("X68GOLEM_TEST_DISK 沒設")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("讀不到 %s：%v", path, err)
	}
	fs, err := NewFAT(raw[256:])
	if err != nil {
		t.Fatal(err)
	}
	root := fs.Root()
	if len(root) == 0 {
		t.Fatal("根目錄是空的")
	}
	t.Logf("退回預設參數：%v，根目錄 %d 筆", fs.Fallback, len(root))
	for _, e := range root {
		t.Logf("  %-14s %8d attr=%02X clu=%d", e.Name, e.Size, e.Attr, e.Cluster)
	}
}
