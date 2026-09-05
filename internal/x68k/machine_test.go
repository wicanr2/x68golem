package x68k

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/wicanr2/x68golem/internal/human68k"
)

// buildZ 造一份合法的 `.Z`，內容是呼叫端給的指令字。
// **不用原版檔案**——服務攔截的正確性該由自己造得出來的樣本證明。
func buildZ(base uint32, words ...uint16) []byte {
	text := make([]byte, len(words)*2)
	for i, w := range words {
		binary.BigEndian.PutUint16(text[i*2:], w)
	}
	h := make([]byte, human68k.ZHeaderSize)
	binary.BigEndian.PutUint16(h[0:], 0x601A)
	binary.BigEndian.PutUint32(h[0x02:], uint32(len(text)))
	binary.BigEndian.PutUint32(h[0x06:], 0)
	binary.BigEndian.PutUint32(h[0x0A:], 0x100)
	binary.BigEndian.PutUint32(h[0x16:], base)
	binary.BigEndian.PutUint16(h[0x1A:], 0xFFFF)
	return append(h, text...)
}

func newTestMachine(t *testing.T, words ...uint16) *Machine {
	t.Helper()
	im, err := human68k.ParseZ(buildZ(0x1000, words...))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMachine(im, DefaultRAMSize)
	if err != nil {
		t.Fatal(err)
	}
	m.Bus.StrictIO = true
	return m
}

func runUntilErr(t *testing.T, m *Machine, max int) error {
	t.Helper()
	for i := 0; i < max; i++ {
		if err := m.Step(); err != nil {
			return err
		}
	}
	return nil
}

// 沒登記的 DOS call 要**停下來並指名是哪一個**，不可以安靜地回 0 繼續跑。
func TestDOSCallUnimplementedStops(t *testing.T) {
	// moveq #5,d0 ; $FF20（_SUPER）; nop
	m := newTestMachine(t, 0x7005, 0xFF20, 0x4E71)
	err := runUntilErr(t, m, 20)
	var un *ErrUnimplemented
	if !errors.As(err, &un) {
		t.Fatalf("要的是 ErrUnimplemented，拿到 %v", err)
	}
	if un.Service.Kind != "DOS call" || un.Service.Number != 0x20 {
		t.Fatalf("記成 %s $%02X", un.Service.Kind, un.Service.Number)
	}
	if un.Service.Name != "_SUPER" {
		t.Errorf("名稱是 %q", un.Service.Name)
	}
	if un.Service.FirstPC != 0x1002 {
		t.Errorf("站點記成 0x%X，應該是 F-line 那一道自己的位址 0x1002", un.Service.FirstPC)
	}
}

// 登記過的 DOS call 要做完、回到 F-line 的**下一道**指令繼續跑。
func TestDOSCallResumesAfterInstruction(t *testing.T) {
	// $FF20 ; moveq #7,d1 ; $FF20 ; nop...
	m := newTestMachine(t, 0xFF20, 0x7207, 0xFF20, 0x4E71, 0x4E71)
	calls := 0
	m.DOSCalls[0x20] = func(mm *Machine) error {
		calls++
		mm.CPU.State.D[0] = 0xABCD
		return nil
	}
	if err := runUntilErr(t, m, 40); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("服務被呼叫 %d 次，應該是 2", calls)
	}
	if m.CPU.State.D[0] != 0xABCD {
		t.Errorf("D0 = 0x%X，服務設的值沒留下", m.CPU.State.D[0])
	}
	if m.CPU.State.D[1] != 7 {
		t.Errorf("D1 = %d：兩次呼叫之間那一道 moveq 沒跑到（回錯位址）", m.CPU.State.D[1])
	}
}

// IOCS 走 trap #15，呼叫號在 D0；trap 堆的是**下一道**指令的位址。
func TestIOCSTrap15(t *testing.T) {
	// moveq #$81,d0 ; trap #15 ; moveq #3,d2 ; nop
	m := newTestMachine(t, 0x7081, 0x4E4F, 0x7403, 0x4E71, 0x4E71)
	got := -1
	m.IOCSCalls[0x81] = func(mm *Machine) error {
		got = int(mm.CPU.State.D[0] & 0xFF)
		return nil
	}
	if err := runUntilErr(t, m, 40); err != nil {
		t.Fatal(err)
	}
	if got != 0x81 {
		t.Fatalf("IOCS 呼叫號讀成 0x%X", got)
	}
	if m.CPU.State.D[2] != 3 {
		t.Errorf("D2 = %d：trap 之後沒回到下一道指令", m.CPU.State.D[2])
	}
}

func TestIOCSUnimplementedStops(t *testing.T) {
	m := newTestMachine(t, 0x7081, 0x4E4F, 0x4E71)
	err := runUntilErr(t, m, 20)
	var un *ErrUnimplemented
	if !errors.As(err, &un) {
		t.Fatalf("要的是 ErrUnimplemented，拿到 %v", err)
	}
	if un.Service.Kind != "IOCS" || un.Service.Number != 0x81 {
		t.Fatalf("記成 %s $%02X", un.Service.Kind, un.Service.Number)
	}
}

// 沒實作的周邊在 StrictIO 下要停下來，而且要講得出是哪一區。
func TestUnimplementedIOStops(t *testing.T) {
	// move.w #$1234,($00E80028).l  →  33FC 1234 00E8 0028
	m := newTestMachine(t, 0x33FC, 0x1234, 0x00E8, 0x0028, 0x4E71)
	err := runUntilErr(t, m, 20)
	if err == nil {
		t.Fatal("寫 CRTC 應該要停下來")
	}
	if got := Classify(0xE80028); got != RegionCRTC {
		t.Fatalf("0xE80028 分類成 %s", got)
	}
	io := m.Bus.IO()
	if len(io) != 1 || io[0].Address != 0xE80028 || !io[0].Write {
		t.Fatalf("I/O 紀錄 = %+v", io)
	}
}

// -lenient 才可以繼續跑；預設不行。這一項是為了讓「繼續跑」永遠是明示的選擇。
func TestLenientIOContinues(t *testing.T) {
	m := newTestMachine(t, 0x33FC, 0x1234, 0x00E8, 0x0028, 0x4E71, 0x4E71)
	m.Bus.StrictIO = false
	for i := 0; i < 3; i++ {
		if err := m.Step(); err != nil {
			t.Fatalf("第 %d 步：%v", i, err)
		}
	}
	if len(m.Bus.IO()) != 1 {
		t.Fatalf("寫入沒被記下來")
	}
}
