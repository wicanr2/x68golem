package x68k

import (
	"fmt"
	"os"
)

// 2HD 軟碟：1024 bytes／磁區、8 磁區／磁軌、2 面、77 磁軌 ＝ 1,261,568 bytes。
const (
	fdSectorSize    = 1024
	fdSectorsPerTrk = 8
	fdSides         = 2
	fdTracks        = 77
	fdImageSize     = fdSectorSize * fdSectorsPerTrk * fdSides * fdTracks

	// DIMHeaderSize 是 `.DIM` 檔前面那段標頭，後面才是原始磁區。
	DIMHeaderSize = 256
)

// Drive 是一台軟碟機。**沒有磁片就是沒有磁片**——原版磁碟由使用者自備，
// 本儲存庫不含任何映像。
type Drive struct {
	Data      []byte // 原始磁區資料（已經去掉 DIM 標頭）
	WriteProt bool
}

// LoadDIM 讀一個 `.DIM` 映像。
func LoadDIM(path string) (*Drive, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < DIMHeaderSize+fdImageSize {
		return nil, fmt.Errorf("x68k: %s 只有 %d bytes，不是 2HD 的 %d＋標頭",
			path, len(raw), fdImageSize)
	}
	return &Drive{Data: raw[DIMHeaderSize : DIMHeaderSize+fdImageSize], WriteProt: true}, nil
}

// drive 依 PDA（physical drive address）取磁碟機。0x90 是第一台。
func (m *Machine) drive(pda byte) *Drive {
	i := int(pda & 0x0F)
	if i < len(m.Drives) {
		return m.Drives[i]
	}
	return nil
}

// InstallFDD 登記軟碟相關的 IOCS 呼叫。
func (m *Machine) InstallFDD() {
	m.IOCSCalls[0x40] = iocsBSeek
	m.IOCSCalls[0x46] = iocsBRead
	m.IOCSCalls[0x4E] = iocsBDrvchk
}

// iocsBDrvchk 是 `$4E _B_DRVCHK`（d1.hb ＝ PDA、d2.w ＝ 功能）。
//
// 回傳的狀態位元（Data Crystal 的 IOCS 手冊）：
// bit7 LED 閃爍、bit6 禁止退片、bit5 OS 級禁止、bit4 使用者級禁止、
// **bit3 防寫**、bit2 未就緒、**bit1 有磁片**、bit0 磁片沒放好。
//
// SANMAIN.Z 開機時就在等 bit1：
//
//	0x069DA2  jsr    (a2)          ← 包到 _B_DRVCHK
//	0x069DA4  btst   #1,d0
//	0x069DAA  beq.s  -18           ← 沒有磁片就一直等
//
// 功能碼 1–9 是退片與 LED 那一類的控制，我們沒有實體機構，收下就好；
// 每一種功能都回同一份狀態。
func iocsBDrvchk(m *Machine) error {
	pda := byte(m.CPU.State.D[1] >> 8)
	d := m.drive(pda)
	var status uint32
	if d != nil {
		status |= 0x02 // 有磁片
		if d.WriteProt {
			status |= 0x08 // 防寫
		}
	} else {
		status |= 0x04 // 未就緒
	}
	m.SetResult(status)
	return nil
}

// iocsBSeek 是 `$40 _B_SEEK`：把磁頭移到指定位置。我們沒有磁頭。
func iocsBSeek(m *Machine) error {
	m.SetResult(0)
	return nil
}

// fdPosition 拆開 IOCS 的磁碟位置編碼。
//
//	bits 24–31：磁區長度碼（0=128、1=256、2=512、3=1024）
//	bits 16–23：磁軌
//	bits  8–15：面
//	bits  0–7 ：磁區編號（從 1 起算）
//
// **拆出來的值超出範圍就回錯誤**，不要照著算下去：這個編碼是照公開規格
// 假設的，如果假設錯了，錯誤會在這裡當場出現，而不是變成一份讀歪的資料。
func fdPosition(pos uint32) (offset int, err error) {
	lenCode := byte(pos >> 24)
	track := int(pos>>16) & 0xFF
	side := int(pos>>8) & 0xFF
	sector := int(pos) & 0xFF
	if lenCode != 3 {
		return 0, fmt.Errorf("x68k: 磁區長度碼 %d（只做 3＝1024 bytes）", lenCode)
	}
	if track < 0 || track >= fdTracks || side < 0 || side >= fdSides ||
		sector < 1 || sector > fdSectorsPerTrk {
		return 0, fmt.Errorf("x68k: 磁碟位置 0x%08X 拆成磁軌 %d 面 %d 磁區 %d，超出 2HD 的範圍",
			pos, track, side, sector)
	}
	lba := (track*fdSides+side)*fdSectorsPerTrk + (sector - 1)
	return lba * fdSectorSize, nil
}

// iocsBRead 是 `$46 _B_READ`（d1 ＝ PDA＋模式、d2 ＝ 位置、d3 ＝ byte 數、
// a1 ＝ 緩衝區）：從磁碟讀資料。
func iocsBRead(m *Machine) error {
	pda := byte(m.CPU.State.D[1] >> 8)
	d := m.drive(pda)
	if d == nil {
		m.SetResult(0xFFFFFFFF) // 沒有磁碟機
		return nil
	}
	off, err := fdPosition(m.CPU.State.D[2])
	if err != nil {
		return err
	}
	n := int(m.CPU.State.D[3])
	buf := m.CPU.State.A[1]
	if off+n > len(d.Data) {
		return fmt.Errorf("x68k: _B_READ 讀到映像外（偏移 %d ＋ %d bytes）", off, n)
	}
	for i := 0; i < n; i++ {
		if err := m.Bus.WriteByte(buf+uint32(i), d.Data[off+i], 5); err != nil {
			return err
		}
	}
	m.SetResult(0)
	return nil
}
