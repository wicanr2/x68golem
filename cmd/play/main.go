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
		drivers = flag.String("drivers", "",
			"先跑這些 `.X` 驅動（逗號分隔）。載入 FLOAT2.X 之後亂數由它提供，"+
				"-rand-fixed 就不再有作用")
		passthru = flag.Bool("rand-passthrough", false,
			"亂數走原版的 FLOAT2.X（要配 -drivers）")
		seq     = flag.String("seq", "", `按鍵序列（\r 是 Return）`)
		out     = flag.String("out", "workplace/out", "截圖與 dump 的輸出目錄")
		settle  = flag.Uint64("settle", 2_000_000, "觀察窗有多少個週期；連續兩個窗變動很少就算畫面靜了")
		perKey    = flag.Int("max-per-key", 400_000_000, "每一鍵最多跑幾道指令")
		minChange = flag.Int("min-change", 64,
			"要等到累計有這麼多個 text VRAM 位址真的變過，才開始判斷「畫完了」。"+
				"少了這一關會在畫面清空、還沒畫下一頁的空檔誤判")
		randVal = flag.Uint64("rand-fixed", 12345, "把 rand() 固定成這個值")
	)
	verbose := flag.Bool("verbose", false, "每個觀察窗印出變動了幾個位址")
	busyOK := flag.Bool("busy-ok", false,
		"畫面一直在動也照樣送鍵。**動畫畫面需要它**——能力值抽取那一頁的數字"+
			"會一直跳（原版就是這樣），永遠不會「靜下來」")
	flag.Parse()
	if *verbose {
		oracle.ScreenWindowLog = func(w, n int) {
			if w <= 60 {
				fmt.Printf("    窗 %3d：變動 %d\n", w, n)
			}
		}
	}
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
	if *passthru {
		cfg.Rand = oracle.RandSource{}
	}
	if *drivers != "" {
		for _, d := range strings.Split(*drivers, ",") {
			cfg.Drivers = append(cfg.Drivers, strings.TrimSpace(d))
		}
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
		if err := o.WaitSettled(*settle, *minChange, *perKey); err != nil {
			if !*busyOK {
				fmt.Fprintf(os.Stderr, "第 %d 鍵之前：%v\n", i, err)
				os.Exit(3)
			}
			fmt.Printf("第 %2d 鍵 0x%02X：畫面一直在動，照樣送（%v）\n", i, r, err)
		}
		save(o, filepath.Join(*out, fmt.Sprintf("step-%02d-before-%02X.png", i, r)))
		fmt.Printf("第 %2d 鍵 0x%02X：畫面已靜（%d 道指令）\n", i, r, o.Steps())
		o.ResetScreenChanges()
		o.Keys(string(r))
		if err := o.Run(2_000_000); err != nil {
			fmt.Fprintf(os.Stderr, "送出第 %d 鍵之後：%v\n", i, err)
			os.Exit(3)
		}
	}
	if err := o.WaitSettled(*settle, *minChange, *perKey); err != nil {
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
