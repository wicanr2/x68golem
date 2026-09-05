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
	"sort"
	"strconv"
	"strings"

	"github.com/wicanr2/x68golem/internal/x68k"
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
		seq       = flag.String("seq", "", `按鍵序列（\r 是 Return）`)
		out       = flag.String("out", "workplace/out", "截圖與 dump 的輸出目錄")
		settle    = flag.Uint64("settle", 2_000_000, "觀察窗有多少個週期；連續兩個窗變動很少就算畫面靜了")
		perKey    = flag.Int("max-per-key", 400_000_000, "每一鍵最多跑幾道指令")
		minChange = flag.Int("min-change", 64,
			"要等到累計有這麼多個 text VRAM 位址真的變過，才開始判斷「畫完了」。"+
				"少了這一關會在畫面清空、還沒畫下一頁的空檔誤判")
		randVal = flag.Uint64("rand-fixed", 12345, "把 rand() 固定成這個值")
		memEnd  = flag.Uint64("mem-end", 0,
			"Human68k 交給程式的記憶體結束位址（管理標頭 +0x08）。"+
				"真機上量到的是 0xFCA86；0 表示用主記憶體大小推")
	)
	verbose := flag.Bool("verbose", false, "每個觀察窗印出變動了幾個位址")
	busyOK := flag.Bool("busy-ok", false,
		"畫面一直在動也照樣送鍵。**動畫畫面需要它**——能力值抽取那一頁的數字"+
			"會一直跳（原版就是這樣），永遠不會「靜下來」")
	watch := flag.String("watch", "", "監看這些位址的寫入（十六進位，逗號分隔）")
	hook := flag.String("hook", "", "在這些位址印出暫存器（十六進位，逗號分隔）")
	hookMax := flag.Int("hook-max", 6, "每個攔截點最多印幾次")
	dumpMem := flag.String("dump-mem", "",
		"跑完之後把主記憶體這些區段寫成檔案（`位址:長度` 十六進位，逗號分隔）")
	ioWrite := flag.String("io-write", "",
		"印出寫進這個位址區間的暫存器（`位址:長度` 十六進位）")
	gvEach := flag.Bool("gvram-each", false,
		"每一鍵之前也把圖形平面存成 gvram-NN.bin（找「哪一步開始不一樣」用）")
	logSvc := flag.String("log-services", "",
		"服務呼叫記錄裡含這個子字串的行就印出來（空字串表示不記錄）")
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
		Rand: oracle.RandSource{Fixed: &fixed}, MemEnd: uint32(*memEnd),
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
	var ioTail []string
	if *ioWrite != "" {
		lo, hi := uint32(0), uint32(0xFFFFFF)
		if a, n, err := parseRange(*ioWrite); err == nil {
			lo, hi = a, a+uint32(n)
		}
		seen := 0
		o.Machine().Bus.OnIOWrite = func(addr uint32, v byte) {
			if addr < lo || addr >= hi {
				return
			}
			seen++
			line := fmt.Sprintf("    寫暫存器 0x%06X ← 0x%02X（PC=0x%06X，第 %d 次）",
				addr, v, o.Machine().Bus.PC, seen)
			if seen <= 20 {
				fmt.Println(line)
			}
			ioTail = append(ioTail, line)
			if len(ioTail) > 20 {
				ioTail = ioTail[1:]
			}
		}
	}

	var watchTail []string
	if *watch != "" {
		b := o.Machine().Bus
		b.Watch = map[uint32]bool{}
		for _, item := range strings.Split(*watch, ",") {
			v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(item), "0x"), 16, 32)
			if err != nil {
				fmt.Fprintf(os.Stderr, "-watch %q 不是十六進位位址\n", item)
				os.Exit(2)
			}
			b.Watch[uint32(v)] = true
		}
		seen := 0
		b.OnWatch = func(addr, v uint32, size int, pc uint32) {
			line := fmt.Sprintf("    監看 0x%06X ← 0x%0*X（%d bytes）PC=0x%06X 第 %d 次",
				addr, size*2, v, size, pc, seen+1)
			seen++
			if seen <= 12 {
				fmt.Println(line)
			}
			// 後面的只留最後幾筆——想看的常常是「誰最後把它蓋掉」。
			watchTail = append(watchTail, line)
			if len(watchTail) > 12 {
				watchTail = watchTail[1:]
			}
		}
	}
	{
		pb := o.Machine().Process.BlockAddr
		v, _ := o.Long(pb + 0x08)
		d30, _ := o.Long(pb + 0x30)
		d38, _ := o.Long(pb + 0x38)
		fmt.Printf("管理區塊 0x%06X：+08=0x%08X +30=0x%08X +38=0x%08X（MemEnd=0x%X）\n",
			pb, v, d30, d38, o.Machine().MemEnd)
	}

	if *logSvc != "" {
		o.Machine().ServiceLog = func(line string) {
			if strings.Contains(line, *logSvc) {
				fmt.Println("    " + line)
			}
		}
	}

	type hookStat struct {
		addr           uint32
		hits           int
		a0Min, a0Max   uint32
		nonZeroSrc     int
		lastA0, lastD0 uint32
	}
	var stats []*hookStat

	if *hook != "" {
		for _, item := range strings.Split(*hook, ",") {
			v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(item), "0x"), 16, 32)
			if err != nil {
				fmt.Fprintf(os.Stderr, "-hook %q 不是十六進位位址\n", item)
				os.Exit(2)
			}
			addr := uint32(v)
			n := 0
			hs := &hookStat{addr: addr, a0Min: ^uint32(0)}
			stats = append(stats, hs)
			o.OnCall(addr, func(f *x68k.Frame) {
				st := f.Machine().CPU.State
				hs.hits++
				if st.A[0] < hs.a0Min {
					hs.a0Min = st.A[0]
				}
				if st.A[0] > hs.a0Max {
					hs.a0Max = st.A[0]
				}
				if st.D[0]&0xFF != 0 {
					hs.nonZeroSrc++
				}
				hs.lastA0, hs.lastD0 = st.A[0], st.D[0]
				if n >= *hookMax {
					return
				}
				n++
				fmt.Printf("    攔截 0x%06X：D0=0x%08X D1=0x%08X D3=0x%08X A0=0x%08X A1=0x%08X A2=0x%08X\n",
					addr, st.D[0], st.D[1], st.D[3], st.A[0], st.A[1], st.A[2])
			})
		}
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
		fmt.Printf("第 %2d 鍵 0x%02X：畫面已靜（%d 道指令，圖形平面非 0 像素 %d）\n",
			i, r, o.Steps(), gvramNonZero(o))
		if *gvEach {
			name := filepath.Join(*out, fmt.Sprintf("gvram-%02d.bin", i))
			if err := os.WriteFile(name, o.GraphicsPlane(), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
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
	g := o.GraphicsPlane()
	if err := os.WriteFile(filepath.Join(*out, "final-gvram.bin"), g, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	nz := 0
	for _, b := range g {
		if b != 0 {
			nz++
		}
	}
	fmt.Printf("graphics VRAM 非 0 bytes：%d／%d\n", nz, len(g))
	reads, opens := 0, 0
	for _, l := range o.Machine().Opens {
		if strings.HasPrefix(l, "  讀 ") {
			reads++
		} else {
			opens++
		}
	}
	fmt.Printf("檔案：開 %d 次、讀 %d 次；DMA 傳送 %d 次、搬了 %d bytes\n",
		opens, reads, o.Machine().DMACTransfers, o.Machine().DMACBytes)
	for _, l := range o.Machine().Opens {
		if strings.HasPrefix(l, "  讀 ") {
			fmt.Println(l)
		}
	}
	{
		svcs := o.Machine().SortedServices()
		fmt.Printf("服務呼叫（%d 種）\n", len(svcs))
		for _, sv := range svcs {
			mark := ""
			if sv.Stubbed {
				mark = "  ⚠回0混過去"
			}
			fmt.Printf("  %-9s $%02X %-10s x%-6d 第一次 PC=0x%06X%s\n",
				sv.Kind, sv.Number, sv.Name, sv.Count, sv.FirstPC, mark)
		}
	}

	{
		fmt.Println("CRTC ／ 視訊控制器的存取：")
		n := 0
		for _, io := range o.Machine().Bus.IO() {
			if io.Region != x68k.RegionCRTC && io.Region != x68k.RegionVideoCtl {
				continue
			}
			n++
			if n > 40 {
				continue
			}
			rw := "讀"
			if io.Write {
				rw = "寫"
			}
			fmt.Printf("  %s 0x%06X（%d bytes）x%-7d 第一次 PC=0x%06X\n",
				rw, io.Address, io.Size, io.Count, io.PC)
		}
		if n > 40 {
			fmt.Printf("  …另外還有 %d 種\n", n-40)
		}
	}

	if n := len(o.Machine().DMACLog); n > 0 {
		fmt.Printf("DMA 啟動 %d 次：\n", n)
		for _, l := range o.Machine().DMACLog {
			fmt.Println("  " + l)
		}
	}

	if n := len(o.Machine().PaintLog); n > 0 {
		fmt.Printf("_PAINT 共塗了 %d 個像素，前 %d 次：\n", o.Machine().PaintPixels, n)
		for _, l := range o.Machine().PaintLog {
			fmt.Println("  " + l)
		}
	}

	if len(ioTail) > 20 {
		fmt.Println("暫存器寫入的最後幾筆：")
		for _, l := range ioTail {
			fmt.Println(l)
		}
	}

	if len(watchTail) > 0 {
		fmt.Println("監看的最後幾筆：")
		for _, l := range watchTail {
			fmt.Println(l)
		}
	}

	for _, hs := range stats {
		if hs.hits == 0 {
			fmt.Printf("攔截 0x%06X：一次都沒到\n", hs.addr)
			continue
		}
		fmt.Printf("攔截 0x%06X：%d 次，A0 0x%06X–0x%06X，D0 低 byte 非 0 的有 %d 次；"+
			"最後一次 A0=0x%06X D0=0x%08X\n",
			hs.addr, hs.hits, hs.a0Min, hs.a0Max, hs.nonZeroSrc, hs.lastA0, hs.lastD0)
	}

	// 誰在寫圖形平面：把 Bus 記到的每一筆 GVRAM 寫入依 PC 併起來。
	{
		type byPC struct {
			pc     uint32
			addrs  int
			writes int
			lo, hi uint32
		}
		agg := map[uint32]*byPC{}
		for _, io := range o.Machine().Bus.IO() {
			if !io.Write || io.Region != x68k.RegionGVRAM {
				continue
			}
			e := agg[io.PC]
			if e == nil {
				e = &byPC{pc: io.PC, lo: io.Address, hi: io.Address}
				agg[io.PC] = e
			}
			e.addrs++
			e.writes += io.Count
			if io.Address < e.lo {
				e.lo = io.Address
			}
			if io.Address > e.hi {
				e.hi = io.Address
			}
		}
		list := make([]*byPC, 0, len(agg))
		for _, e := range agg {
			list = append(list, e)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].writes > list[j].writes })
		if len(list) > 0 {
			fmt.Printf("寫圖形平面的地方（依 PC，共 %d 處）\n", len(list))
			for i, e := range list {
				if i >= 12 {
					break
				}
				fmt.Printf("  PC=0x%06X：%d 次寫入、%d 個相異位址（0x%06X–0x%06X）\n",
					e.pc, e.writes, e.addrs, e.lo, e.hi)
			}
		}
	}

	if *dumpMem != "" {
		for _, item := range strings.Split(*dumpMem, ",") {
			a, n, err := parseRange(item)
			if err != nil {
				fmt.Fprintf(os.Stderr, "-dump-mem %q：%v\n", item, err)
				os.Exit(2)
			}
			ram := o.Machine().Bus.RAM
			if int(a)+n > len(ram) {
				fmt.Fprintf(os.Stderr, "-dump-mem %q 超出記憶體\n", item)
				os.Exit(2)
			}
			seg := ram[a : int(a)+n]
			nz := 0
			for _, b := range seg {
				if b != 0 {
					nz++
				}
			}
			name := filepath.Join(*out, fmt.Sprintf("mem-%06X-%X.bin", a, n))
			if err := os.WriteFile(name, seg, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Printf("記憶體 0x%06X 起 %d bytes：非 0 %d → %s\n", a, n, nz, name)
		}
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

// parseRange 解析 `位址:長度`，兩邊都是十六進位。
func parseRange(s string) (uint32, int, error) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("格式是 位址:長度")
	}
	hex := func(x string) (uint64, error) {
		return strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(x), "0x"), 16, 32)
	}
	a, err := hex(parts[0])
	if err != nil {
		return 0, 0, err
	}
	n, err := hex(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return uint32(a), int(n), nil
}

// gvramNonZero 回傳圖形平面有幾個像素不是索引 0。
func gvramNonZero(o *oracle.Oracle) int {
	g := o.GraphicsPlane()
	n := 0
	for i := 1; i < len(g); i += 2 {
		if g[i] != 0 {
			n++
		}
	}
	return n
}
