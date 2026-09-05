// play 用「等畫面靜下來再按下一個鍵」的方式跑一段操作序列。
//
//	tools/go.sh run ./cmd/play -z SANMAIN.Z -disks A.dim,B.dim \
//	    -cgrom cgrom.dat -seq " \r1\r1\r1\r1\r     y\r5\r1\r2\ry\r " \
//	    -out workplace/out
//
// 與 `cmd/probe -keys` 的差別是**不猜每個畫面要畫多久**：
// probe 用固定延遲，一次實測就因此錯位——送給「電腦強度」的 5 掉進
// 「幾人遊戲」，後面全歪。這支每按一鍵就存一張圖，序列走到哪裡看得見。
package main

import (
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/wicanr2/x68golem/oracle"
)

func main() {
	var (
		z       = flag.String("z", "", "Human68k `.Z` 執行檔（玩家自備）")
		disks   = flag.String("disks", "", "軟碟映像，逗號分隔（玩家自備）")
		cgrom   = flag.String("cgrom", "", "CGROM（玩家自備）")
		seq     = flag.String("seq", "", `按鍵序列（\r 是 Return）`)
		out     = flag.String("out", "workplace/out", "截圖與 dump 的輸出目錄")
		settle  = flag.Uint64("settle", 30_000_000, "畫面靜下來要連續幾個週期沒有寫 text VRAM")
		perKey  = flag.Int("max-per-key", 400_000_000, "每一鍵最多跑幾道指令")
		randVal = flag.Uint64("rand-fixed", 12345, "把 rand() 固定成這個值")
	)
	flag.Parse()
	if *z == "" || *seq == "" {
		fmt.Fprintln(os.Stderr, "要指定 -z 與 -seq。本工具不含任何原版素材。")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fixed := uint32(*randVal)
	cfg := oracle.Config{
		Exe: *z, CGROM: *cgrom, LatchIO: true,
		Rand: oracle.RandSource{Fixed: &fixed},
	}
	if *disks != "" {
		for _, d := range strings.Split(*disks, ",") {
			cfg.Disks = append(cfg.Disks, strings.TrimSpace(d))
		}
	}
	o, err := oracle.Load(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	keys := strings.NewReplacer(`\r`, "\r", `\n`, "\r", `\t`, "\t").Replace(*seq)

	for i, r := range []byte(keys) {
		if err := o.WaitSettled(*settle, *perKey); err != nil {
			fmt.Fprintf(os.Stderr, "第 %d 鍵之前：%v\n", i, err)
			os.Exit(3)
		}
		save(o, filepath.Join(*out, fmt.Sprintf("step-%02d-before-%02X.png", i, r)))
		fmt.Printf("第 %2d 鍵 0x%02X：畫面已靜（%d 道指令）\n", i, r, o.Steps())
		o.Keys(string(r))
		if err := o.Run(2_000_000); err != nil {
			fmt.Fprintf(os.Stderr, "送出第 %d 鍵之後：%v\n", i, err)
			os.Exit(3)
		}
	}
	if err := o.WaitSettled(*settle, *perKey); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	save(o, filepath.Join(*out, "step-final.png"))
	if err := os.WriteFile(filepath.Join(*out, "final-plane0.bin"), o.TextPlane(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("跑完 %d 鍵，共 %d 道指令、%d 個週期\n", len(keys), o.Steps(), o.Cycles())
}

func save(o *oracle.Oracle, path string) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, o.Machine().Bus.TextImage(512, 512)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
