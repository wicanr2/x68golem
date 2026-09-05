package oracle_test

import (
	"os"
	"strings"
	"testing"

	"github.com/wicanr2/x68golem/oracle"
)

// 這一支是 `oracle` 的示範也是驗收：remake 的 `go test` 要能用同一套寫法
// 去問原版問題。需要玩家自備的素材，缺一樣就 skip。
//
//	X68GOLEM_TEST_Z      SANMAIN.Z
//	X68GOLEM_TEST_DISKS  兩個 .DIM，逗號分隔
//	X68GOLEM_TEST_CGROM  cgrom.dat
func newOracle(t *testing.T) *oracle.Oracle {
	t.Helper()
	z := os.Getenv("X68GOLEM_TEST_Z")
	disks := os.Getenv("X68GOLEM_TEST_DISKS")
	cgrom := os.Getenv("X68GOLEM_TEST_CGROM")
	if z == "" || disks == "" || cgrom == "" {
		t.Skip("X68GOLEM_TEST_Z／DISKS／CGROM 沒設齊")
	}
	fixed := uint32(12345)
	o, err := oracle.Load(oracle.Config{
		Exe:      z,
		Disks:    strings.Split(disks, ","),
		CGROM:    cgrom,
		LatchIO:  true,
		Rand:     oracle.RandSource{Fixed: &fixed},
		KeyDelay: 6_000_000,
	})
	if err != nil {
		t.Skipf("開不起來：%v", err)
	}
	return o
}

// 開機到標題畫面，然後從那個狀態展開兩條路——這是快照存在的理由。
func TestOracleSnapshotBranches(t *testing.T) {
	o := newOracle(t)
	o.Keys(" \r")
	if err := o.Run(200_000_000); err != nil {
		t.Fatal(err)
	}
	if n := nonZero(o.TextPlane()); n < 200 {
		t.Fatalf("標題畫面只有 %d 個非 0 bytes", n)
	}
	snap := o.Snapshot()
	before := append([]byte(nil), o.TextPlane()...)

	// 分支一：選 1（NEW GAME）
	o.Keys("1\r")
	if err := o.Run(200_000_000); err != nil {
		t.Fatal(err)
	}
	branch := append([]byte(nil), o.TextPlane()...)
	if string(branch) == string(before) {
		t.Fatal("按了 1 之後畫面沒變，這個測試沒在測東西")
	}

	// 回到標題畫面：畫面要與快照當時**逐位元組相同**
	o.Restore(snap)
	if string(o.TextPlane()) != string(before) {
		t.Error("回復之後畫面與快照當時不同")
	}
}

func nonZero(b []byte) int {
	n := 0
	for _, v := range b {
		if v != 0 {
			n++
		}
	}
	return n
}
