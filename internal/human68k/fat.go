package human68k

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Human68k 的軟碟是 FAT12，但**不要只信 BPB**：
// 已知《三國志》Disk A 的 BPB 欄位是非標準值
// （`sangokushi_x68k_cht` 的 `CLAUDE.md` §2.1）。所以讀不出合理值時
// 退回 2HD 的標準參數，而不是照著壞值算下去。
const (
	fallbackBytesPerSector = 1024
	fallbackSecPerClus     = 1
	fallbackReserved       = 1
	fallbackNumFATs        = 2
	fallbackRootEntries    = 192
	fallbackSecPerFAT      = 2
)

// FAT 是一份唯讀的 Human68k FAT12 檔案系統。
type FAT struct {
	data []byte

	BytesPerSector uint32
	SecPerClus     uint32
	Reserved       uint32
	NumFATs        uint32
	RootEntries    uint32
	SecPerFAT      uint32
	// Fallback 為真表示 BPB 讀不出合理值，用了 2HD 的標準參數。
	Fallback bool

	fatOff  uint32
	rootOff uint32
	dataOff uint32
}

// DirEntry 是根目錄裡的一筆。
//
// Human68k 的目錄項目比 MS-DOS 多一段：主檔名 8 bytes 之後，
// 0x0C 起還有 10 bytes 的續接，合起來最長 18 字。
type DirEntry struct {
	Name    string // 已經組好的「主檔名.副檔名」
	Attr    byte
	Cluster uint16
	Size    uint32
}

// NewFAT 把一份原始磁區資料（DIM 標頭已經去掉）當成 FAT12 打開。
func NewFAT(data []byte) (*FAT, error) {
	if len(data) < 0x200 {
		return nil, fmt.Errorf("human68k: 磁碟資料只有 %d bytes", len(data))
	}
	f := &FAT{data: data}
	f.BytesPerSector = uint32(binary.LittleEndian.Uint16(data[11:]))
	f.SecPerClus = uint32(data[13])
	f.Reserved = uint32(binary.LittleEndian.Uint16(data[14:]))
	f.NumFATs = uint32(data[16])
	f.RootEntries = uint32(binary.LittleEndian.Uint16(data[17:]))
	f.SecPerFAT = uint32(binary.LittleEndian.Uint16(data[22:]))

	if f.BytesPerSector != 512 && f.BytesPerSector != 1024 {
		f.Fallback = true
	}
	if f.SecPerClus == 0 || f.NumFATs == 0 || f.SecPerFAT == 0 || f.RootEntries == 0 {
		f.Fallback = true
	}
	if f.Fallback {
		f.BytesPerSector = fallbackBytesPerSector
		f.SecPerClus = fallbackSecPerClus
		f.Reserved = fallbackReserved
		f.NumFATs = fallbackNumFATs
		f.RootEntries = fallbackRootEntries
		f.SecPerFAT = fallbackSecPerFAT
	}
	f.fatOff = f.Reserved * f.BytesPerSector
	f.rootOff = (f.Reserved + f.NumFATs*f.SecPerFAT) * f.BytesPerSector
	f.dataOff = f.rootOff + f.RootEntries*32
	if f.dataOff >= uint32(len(data)) {
		return nil, fmt.Errorf("human68k: 算出來的資料區起點 0x%X 超出映像", f.dataOff)
	}
	return f, nil
}

// Root 列出根目錄。刪除的、標成磁碟區名稱的都跳過。
func (f *FAT) Root() []DirEntry {
	var out []DirEntry
	for i := uint32(0); i < f.RootEntries; i++ {
		e := f.data[f.rootOff+i*32 : f.rootOff+i*32+32]
		if e[0] == 0 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x08 != 0 {
			continue
		}
		out = append(out, DirEntry{
			Name:    entryName(e),
			Attr:    e[11],
			Cluster: binary.LittleEndian.Uint16(e[26:]),
			Size:    binary.LittleEndian.Uint32(e[28:]),
		})
	}
	return out
}

// entryName 把目錄項目組成「主檔名.副檔名」。
func entryName(e []byte) string {
	main := strings.TrimRight(string(e[0:8]), " \x00")
	// Human68k 的續接欄位：0x0C 起 10 bytes，沒有就是 0 或空白。
	main += strings.TrimRight(string(e[12:22]), " \x00")
	ext := strings.TrimRight(string(e[8:11]), " \x00")
	if ext == "" {
		return main
	}
	return main + "." + ext
}

// Find 依檔名找一筆（不分大小寫，可以帶或不帶磁碟機與路徑前綴）。
func (f *FAT) Find(name string) (DirEntry, bool) {
	want := normalizeName(name)
	for _, e := range f.Root() {
		if normalizeName(e.Name) == want {
			return e, true
		}
	}
	return DirEntry{}, false
}

func normalizeName(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if i := strings.LastIndexAny(s, ":\\/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// next 讀 FAT12 的下一個叢集。
func (f *FAT) next(clus uint16) uint16 {
	off := f.fatOff + uint32(clus)*3/2
	if off+1 >= uint32(len(f.data)) {
		return 0xFFF
	}
	v := uint16(f.data[off]) | uint16(f.data[off+1])<<8
	if clus&1 == 0 {
		return v & 0x0FFF
	}
	return v >> 4
}

// Read 把一筆檔案的內容讀出來。
func (f *FAT) Read(e DirEntry) ([]byte, error) {
	clusSize := f.SecPerClus * f.BytesPerSector
	out := make([]byte, 0, e.Size)
	clus := e.Cluster
	for len(out) < int(e.Size) {
		if clus < 2 || clus >= 0xFF0 {
			return nil, fmt.Errorf("human68k: %s 的叢集鏈在 0x%03X 斷掉（已讀 %d／%d bytes）",
				e.Name, clus, len(out), e.Size)
		}
		off := f.dataOff + uint32(clus-2)*clusSize
		if off+clusSize > uint32(len(f.data)) {
			return nil, fmt.Errorf("human68k: %s 的叢集 %d 超出映像", e.Name, clus)
		}
		out = append(out, f.data[off:off+clusSize]...)
		clus = f.next(clus)
	}
	return out[:e.Size], nil
}
