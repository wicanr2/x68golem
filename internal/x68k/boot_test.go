package x68k

import (
	"os"
	"path/filepath"
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
	m, err := NewMachine(im, DefaultRAMSize, filepath.Base(z))
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

	compareWithMAME(t, m.Bus.TVRAM[:0x20000])
	compareGraphicsWithMAME(t, m.Bus.GVRAM[:0x80000])
}

// compareGraphicsWithMAME 把圖形平面與 MAME 在同一個畫面 dump 的那一份逐位元組比。
//
//	X68GOLEM_TEST_MAME_GVRAM  MAME 的 dump（512 KB，同一支 dump-tvram.lua 產生）
//
// 這裡**不留任何容差**。文字平面要放過游標的閃爍相位，圖形平面沒有那種東西：
// 畫面是靜止的，一個 byte 不同就是畫錯了。
//
// 兩件事要成立這一比才過得了（都是量出來的，不是查表）：
//
//   - 256 色模式下第 0 頁的 word 只有低 byte 是真的記憶體（`internal/x68k/bus.go`
//     的 GraphicsBPP，`docs/findings/020`）。
//   - 換畫面時的清除是 CRTC 的高速クリア，不是 CPU 迴圈（`docs/findings/022`）。
func compareGraphicsWithMAME(t *testing.T, ours []byte) {
	t.Helper()
	ref := os.Getenv("X68GOLEM_TEST_MAME_GVRAM")
	if ref == "" {
		t.Log("X68GOLEM_TEST_MAME_GVRAM 沒設，跳過圖形平面與 MAME 的逐位元組比對")
		return
	}
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Skipf("讀不到 %s：%v", ref, err)
	}
	if len(want) != len(ours) {
		t.Fatalf("MAME 的 dump 是 %d bytes，我們的是 %d", len(want), len(ours))
	}
	diffs, first := 0, -1
	for i := range ours {
		if ours[i] != want[i] {
			diffs++
			if first < 0 {
				first = i
			}
		}
	}
	if diffs != 0 {
		px := first / 2
		t.Fatalf("圖形平面與 MAME 有 %d bytes 不同，第一筆在 (%d,%d)：我們 0x%02X，MAME 0x%02X",
			diffs, px%512, px/512, ours[first], want[first])
	}
	nz := 0
	for i := 1; i < len(ours); i += 2 {
		if ours[i] != 0 {
			nz++
		}
	}
	t.Logf("圖形平面與 MAME 逐位元組相同（%d 個非 0 像素）", nz)
}

// compareWithMAME 把我們的 text VRAM 平面 0 與 MAME 在同一個畫面 dump 的
// 那一份逐位元組比。
//
//	X68GOLEM_TEST_MAME_TVRAM  MAME 的 dump（128 KB，
//	                          由 sangokushi_x68k_cht 的 tools/x68k-scripts/dump-tvram.lua 產生）
//
// 允許的唯一差異是**游標的閃爍相位**：游標是一個 8×16 的方塊，
// 在平面 0 裡就是同一個 byte 欄位連續 16 列。相位取決於我們的垂直同步時間軸
// 與 MAME 的差異，不是畫錯——所以這裡驗的是「差異只有那一塊」，
// 而不是「完全相同」。多一個 byte 落在別的地方就是真的畫錯了。
func compareWithMAME(t *testing.T, ours []byte) {
	t.Helper()
	ref := os.Getenv("X68GOLEM_TEST_MAME_TVRAM")
	if ref == "" {
		t.Log("X68GOLEM_TEST_MAME_TVRAM 沒設，跳過與 MAME 的逐位元組比對")
		return
	}
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Skipf("讀不到 %s：%v", ref, err)
	}
	if len(want) != len(ours) {
		t.Fatalf("MAME 的 dump 是 %d bytes，我們的是 %d", len(want), len(ours))
	}
	const rowBytes = 128
	var diffs []int
	for i := range ours {
		if ours[i] != want[i] {
			diffs = append(diffs, i)
		}
	}
	if len(diffs) == 0 {
		t.Log("與 MAME 完全相同")
		return
	}
	col := diffs[0] % rowBytes
	row0 := diffs[0] / rowBytes
	for n, i := range diffs {
		if i%rowBytes != col || i/rowBytes != row0+n {
			t.Fatalf("與 MAME 的差異不只游標那一塊：%d 筆，第一筆在 y=%d x=%d，"+
				"第 %d 筆在 y=%d x=%d", len(diffs), row0, col*8, n, i/rowBytes, (i%rowBytes)*8)
		}
	}
	if len(diffs) > 16 {
		t.Fatalf("差異 %d bytes，超過一個 8×16 的游標方塊", len(diffs))
	}
	t.Logf("與 MAME 只差 %d bytes，全部落在 y=%d..%d x=%d 的一塊——游標的閃爍相位",
		len(diffs), row0, row0+len(diffs)-1, col*8)
}
