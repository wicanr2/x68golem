package x68k

import (
	"os"
	"strings"
	"testing"

	"github.com/wicanr2/x68golem/internal/human68k"
)

// TestBootToTitle 把原版開機到標題畫面。
//
// **需要玩家自備的素材，缺一樣就 skip**——本儲存庫不含執行檔、磁碟或 CGROM。
//
//	X68GOLEM_TEST_Z      SANMAIN.Z
//	X68GOLEM_TEST_DISKS  兩個 .DIM，逗號分隔
//	X68GOLEM_TEST_CGROM  cgrom.dat
//
// 斷言的是兩個**數字**，不是「看起來對」：
//
//   - 走過的相異位址要超過 3,500。這比「跑了幾道指令」誠實：卡住的時候
//     指令數照樣會漲，相異位址不會。
//   - 文字平面的非 0 像素要超過 500。標題畫面那一行
//     `1:NEW GAME  2:LOAD DATA (1-2)?` 大約是 718 個。
//
// 兩個門檻都刻意留寬：這支是回歸測試，要抓的是「又壞回去了」，
// 不是逐像素比對——逐像素的對照 oracle 是 MAME（`docs/spec/003`）。
func TestBootToTitle(t *testing.T) {
	z := os.Getenv("X68GOLEM_TEST_Z")
	disks := os.Getenv("X68GOLEM_TEST_DISKS")
	cgrom := os.Getenv("X68GOLEM_TEST_CGROM")
	if z == "" || disks == "" || cgrom == "" {
		t.Skip("X68GOLEM_TEST_Z／DISKS／CGROM 沒設齊，跳過原版開機")
	}
	data, err := os.ReadFile(z)
	if err != nil {
		t.Skipf("讀不到 %s：%v", z, err)
	}
	im, err := human68k.ParseZ(data)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMachine(im, DefaultRAMSize)
	if err != nil {
		t.Fatal(err)
	}
	m.InstallDOSCalls()
	m.InstallIOCS()
	m.InstallConsole()
	m.InstallFDD()
	m.InstallFiles()
	m.InstallSprite()
	m.InstallFloat()
	m.InstallVDisp()
	m.InstallKeyboard()
	for _, p := range strings.Split(disks, ",") {
		d, err := LoadDIM(strings.TrimSpace(p))
		if err != nil {
			t.Skipf("讀不到磁碟 %s：%v", p, err)
		}
		m.Drives = append(m.Drives, d)
	}
	rom, err := os.ReadFile(cgrom)
	if err != nil {
		t.Skipf("讀不到 CGROM：%v", err)
	}
	m.Bus.CGROM = rom
	m.Bus.LatchIO = true
	m.Bus.StrictIO = false
	m.RNG.Mode = RNGFixed
	m.RNG.Value = 12345
	m.HotPC = map[uint32]int{}

	// 空白鍵跳過第一個畫面，Return 通過亂數初始化。
	// 兩鍵之間要隔一段時間——遊戲拿等待時間當種子（keyboard.go）。
	m.Keys.Delay = 20_000_000
	m.Keys.PushString(" \r")

	for i := 0; i < 200_000_000; i++ {
		if err := m.Step(); err != nil {
			t.Fatalf("第 %d 步（走過 %d 個位址）：%v", i, len(m.HotPC), err)
		}
	}
	if got := len(m.HotPC); got < 3500 {
		t.Errorf("只走過 %d 個相異位址，開機沒有走到標題畫面", got)
	}
	if got := m.Bus.TextNonZero(512, 512); got < 500 {
		t.Errorf("文字平面只有 %d 個非 0 像素，畫面等於空白", got)
	}
}
