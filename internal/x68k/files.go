package x68k

import (
	"fmt"

	"github.com/wicanr2/x68golem/internal/human68k"
)

// Human68k 的檔案服務。目前**唯讀**：`_OPEN`／`_READ`／`_SEEK`／`_CLOSE`。
// 寫入（`_CREATE`／`_WRITE`）還沒做——存檔會走到那裡，做的時候要一起想
// 「寫回磁碟映像」這件事該不該發生（對拍時多半不該）。
//
// 參數都在堆疊上，第一個參數在 SP+0（C 的慣例，由右往左推）。

// openFile 是一個開著的檔案。
type openFile struct {
	name string
	data []byte
	pos  int
}

const firstFileHandle = 5 // 0–4 留給標準裝置

// Human68k 的錯誤碼（負值）。只列用得到的。
const (
	errFileNotFound = 0xFFFFFFFE // −2
	errTooManyOpen  = 0xFFFFFFFC // −4
	errBadHandle    = 0xFFFFFFF9 // −7
)

// InstallFiles 登記檔案相關的 DOS call。
func (m *Machine) InstallFiles() {
	m.DOSCalls[0x3D] = dosOpen
	m.DOSCalls[0x3E] = dosClose
	m.DOSCalls[0x3F] = dosRead
	m.DOSCalls[0x42] = dosSeek
}

// readString 從記憶體讀一個 null 結尾字串。
func (m *Machine) readString(addr uint32, max int) (string, error) {
	var b []byte
	for i := 0; i < max; i++ {
		c, err := m.Bus.ReadByte(addr+uint32(i), 5)
		if err != nil {
			return "", err
		}
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b), nil
}

// findOnDisks 依檔名在每一台磁碟機裡找。
//
// 沒有目前磁碟機的概念（Human68k 有，我們還沒需要）：檔名帶不帶
// `A:` 前綴都一樣，從 0 號機開始找第一個對得上的。
// **這是簡化，不是等價**——兩片磁碟有同名檔案時會拿到前面那一片的。
func (m *Machine) findOnDisks(name string) ([]byte, error) {
	for i, d := range m.Drives {
		if d == nil {
			continue
		}
		fs, err := human68k.NewFAT(d.Data)
		if err != nil {
			return nil, fmt.Errorf("%d 號磁碟機：%w", i, err)
		}
		if e, ok := fs.Find(name); ok {
			return fs.Read(e)
		}
	}
	return nil, nil
}

func dosOpen(m *Machine) error {
	ptr, err := m.ArgLongAt(0)
	if err != nil {
		return err
	}
	mode, err := m.ArgWord(4)
	if err != nil {
		return err
	}
	if mode&0x03 != 0 {
		return fmt.Errorf("_OPEN：模式 %d 要求寫入，目前只做唯讀", mode)
	}
	name, err := m.readString(ptr, 128)
	if err != nil {
		return err
	}
	data, err := m.findOnDisks(name)
	if err != nil {
		return err
	}
	if data == nil {
		m.Opens = append(m.Opens, name+"（找不到）")
		m.SetResult(errFileNotFound)
		return nil
	}
	if m.files == nil {
		m.files = map[uint16]*openFile{}
	}
	for h := uint16(firstFileHandle); h < 64; h++ {
		if _, taken := m.files[h]; !taken {
			m.files[h] = &openFile{name: name, data: data}
			m.Opens = append(m.Opens, fmt.Sprintf("%s（%d bytes）", name, len(data)))
			m.SetResult(uint32(h))
			return nil
		}
	}
	m.SetResult(errTooManyOpen)
	return nil
}

func dosClose(m *Machine) error {
	h, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	if _, ok := m.files[h]; !ok {
		m.SetResult(errBadHandle)
		return nil
	}
	delete(m.files, h)
	m.SetResult(0)
	return nil
}

func dosRead(m *Machine) error {
	h, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	buf, err := m.ArgLongAt(2)
	if err != nil {
		return err
	}
	n, err := m.ArgLongAt(6)
	if err != nil {
		return err
	}
	f, ok := m.files[h]
	if !ok {
		m.SetResult(errBadHandle)
		return nil
	}
	end := f.pos + int(n)
	if end > len(f.data) {
		end = len(f.data)
	}
	for i := f.pos; i < end; i++ {
		if err := m.Bus.WriteByte(buf+uint32(i-f.pos), f.data[i], 5); err != nil {
			return err
		}
	}
	got := end - f.pos
	m.Opens = append(m.Opens, fmt.Sprintf("  讀 %s：位置 %d 起 %d bytes（要 %d）",
		f.name, f.pos, got, n))
	f.pos = end
	m.SetResult(uint32(got))
	return nil
}

func dosSeek(m *Machine) error {
	h, err := m.ArgWord(0)
	if err != nil {
		return err
	}
	off, err := m.ArgLongAt(2)
	if err != nil {
		return err
	}
	mode, err := m.ArgWord(6)
	if err != nil {
		return err
	}
	f, ok := m.files[h]
	if !ok {
		m.SetResult(errBadHandle)
		return nil
	}
	var pos int64
	switch mode {
	case 0:
		pos = int64(int32(off))
	case 1:
		pos = int64(f.pos) + int64(int32(off))
	case 2:
		pos = int64(len(f.data)) + int64(int32(off))
	default:
		return fmt.Errorf("_SEEK：模式 %d 不存在", mode)
	}
	if pos < 0 || pos > int64(len(f.data)) {
		m.SetResult(errBadHandle)
		return nil
	}
	f.pos = int(pos)
	m.SetResult(uint32(pos))
	return nil
}
