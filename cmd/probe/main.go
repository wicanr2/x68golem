// probe 是 M0：把 SANMAIN.Z（或任何 Human68k `.Z`）跑起來，
// 列出它**用到而我們還沒實作**的 DOS call、IOCS 與硬體位址。
//
//	tools/go.sh run ./cmd/probe -z /orig/orig/x68k/SANMAIN.Z
//
// 為什麼要用「跑」而不是「靜態掃」：靜態掃已經有人做過了
// （IDA 的普查），而它自己就講明是下界——`trap #15` 的 byte 掃描
// 在 289 個候選裡只有 28 個是真的，其餘落在分支位移與資料裡。
// 執行期的紀錄沒有這個問題：跑到了就是跑到了。
//
// 代價是走得多遠取決於實作了多少。**這正是它要回答的問題**：
// 下一個要做的是哪一個服務。
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"image/png"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/wicanr2/x68golem/internal/human68k"
	"github.com/wicanr2/x68golem/internal/x68k"
)

// installStubs 把 -stub 指定的服務接成「回 0」。
//
// 它與 -lenient 的差別是**指名**：報告裡照樣會標成回 0 混過去，
// 但沒被指名的服務仍然會讓執行停下來。探一段路而不弄髒其他結論。
func installStubs(m *x68k.Machine, spec string) error {
	if spec == "" {
		return nil
	}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		kind, num, ok := strings.Cut(item, ":")
		if !ok {
			return fmt.Errorf("-stub 的格式是 dos:44 或 iocs:0E，看不懂 %q", item)
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(num, "$"), 16, 16)
		if err != nil {
			return fmt.Errorf("-stub %q 的呼叫號不是十六進位數：%v", item, err)
		}
		fn := func(mm *x68k.Machine) error { mm.SetResult(0); mm.MarkStubbed(); return nil }
		switch strings.ToLower(kind) {
		case "dos":
			m.DOSCalls[uint16(n)] = fn
		case "iocs":
			m.IOCSCalls[uint16(n)] = fn
		default:
			return fmt.Errorf("-stub 的種類只有 dos 與 iocs，看不懂 %q", kind)
		}
	}
	return nil
}

// scanFloatCalls 掃出映像裡的 $FE00–$FEFF。
//
// ⚠ **字對齊的線性掃描會有假陽性**：`$FExx` 這個位元組樣式也會落在分支
// 位移與資料裡（IDA 對 `trap #15` 做同樣的掃描時，289 個候選裡只有 28 個
// 是真的）。所以這裡分兩組報：
//
//   - **後面緊接著 `rts`（$4E75）的**：這個遊戲的浮點呼叫全部包在
//     `move.l (4,sp),d0 / $FExx / rts` 這種三行小函式裡，所以這一組
//     的可信度高很多。
//   - 其餘：只當線索。
//
// 掃描的目的不是「有幾個」，是「用到哪幾種運算」——那決定要自己實作
// 還是把 FLOAT2.X 載進來跑（docs/findings/004）。
func scanFloatCalls(im *human68k.Image) {
	type info struct {
		withRTS, other int
		at             []uint32
	}
	seen := map[uint16]*info{}
	body := im.Body
	limit := int(im.TextSize)
	if limit > len(body) {
		limit = len(body)
	}
	for off := 0; off+3 < limit; off += 2 {
		w := uint16(body[off])<<8 | uint16(body[off+1])
		if !human68k.IsFloatCall(w) {
			continue
		}
		e := seen[w&0xFF]
		if e == nil {
			e = &info{}
			seen[w&0xFF] = e
		}
		next := uint16(body[off+2])<<8 | uint16(body[off+3])
		if next == 0x4E75 {
			e.withRTS++
			e.at = append(e.at, im.Base+uint32(off))
		} else {
			e.other++
		}
	}
	var codes []int
	for c := range seen {
		codes = append(codes, int(c))
	}
	sort.Ints(codes)
	fmt.Printf("== FLOAT2（$FE00–$FEFF）掃描：%d 種號碼\n", len(codes))
	fmt.Println("   後接 rts 的可信度高；其餘只是線索（位元組樣式也會落在位移與資料裡）")
	for _, c := range codes {
		e := seen[uint16(c)]
		fmt.Printf("  $FE%02X  後接rts %-4d 其他 %-4d %v\n", c, e.withRTS, e.other, hexAddrs(e.at))
	}
}

func hexAddrs(a []uint32) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = fmt.Sprintf("0x%06X", v)
	}
	return out
}

// dumpWords 印出映像裡某個位址起的 n 個字，給人看程式碼用。
func dumpWords(im *human68k.Image, addr uint32, n int) {
	for i := 0; i < n; i++ {
		a := addr + uint32(i*2)
		off := int(a - im.Base)
		if off < 0 || off+1 >= len(im.Body) {
			fmt.Printf("  0x%06X  （超出映像）\n", a)
			return
		}
		fmt.Printf("  0x%06X  %04X\n", a, uint16(im.Body[off])<<8|uint16(im.Body[off+1]))
	}
}

func main() {
	var (
		zPath    = flag.String("z", "", "Human68k `.Z` 執行檔（玩家自備）")
		maxSteps = flag.Uint64("steps", 50_000_000, "最多執行幾道指令")
		ram      = flag.Int("ram", x68k.DefaultRAMSize, "主記憶體大小（bytes）")
		randFixed = flag.Int("rand-fixed", -1,
			"把 rand() 固定成這個值。不指定就 fail-closed——"+
				"FLOAT2.X 的 rand() 演算法還沒解出來，沒有直通模式")
		find = flag.String("find", "",
			"在映像裡找一段位元組（十六進位，例如 4EB90006F28E），印出所有位址")
		dumpAt = flag.String("dump", "",
			"印出映像裡某個位址起的字，格式 0x06A180:12")
		scanFloat = flag.Bool("scan-float", false,
			"掃映像裡的 $FE00–$FEFF（FLOAT2.X 的浮點呼叫）並列出號碼")
		disks = flag.String("disks", "",
			"軟碟映像（`.DIM`），逗號分隔，依序放進 0 號、1 號磁碟機。玩家自備")
		keys = flag.String("keys", "",
			"預先排進鍵盤佇列的字元（\\n 代表 Return）")
		watch = flag.String("watch", "",
			"監看這些主記憶體位址的寫入（十六進位，逗號分隔），印出 PC 與值")
		watchMax = flag.Int("watch-max", 40, "最多印幾筆監看紀錄")
		hot = flag.Int("hot", 0, "印出執行次數最多的 N 個位址（回答「它卡在哪」）")
		shot = flag.String("shot", "",
			"停下來時把文字平面存成 PNG")
		shotW = flag.Int("shot-width", 512, "截圖寬度")
		shotH = flag.Int("shot-height", 512, "截圖高度")
		logSvc = flag.Int("log-services", 0,
			"把最後 N 次服務呼叫連同 SR／堆疊指標印出來")
		trace = flag.Int("trace", 0, "停下來時印出最後 N 道指令與暫存器")
		latch = flag.Bool("latch-io", false,
			"把還沒實作的周邊暫存器當成單純的閂鎖（寫什麼就讀得回什麼）。"+
				"讓「寫了自己再讀回來確認」的等待迴圈走得完；不是模擬硬體")
		stopIO = flag.String("stop-io", "",
			"碰到這些 I/O 位址就停（十六進位，逗號分隔）。停下來時軌跡還在，"+
				"用來回答「它是怎麼走到這個暫存器的」")
		stub  = flag.String("stub", "",
			"指名要「回 0 混過去」的服務，例如 dos:44,iocs:0E。"+
				"這是探路用的：想看某個服務之後的程式碼長什麼樣，但又不想像 "+
				"-lenient 那樣把整份報告變成不可信")
		lenient = flag.Bool("lenient", false,
			"沒實作的 I/O 與服務一律回 0 繼續跑（產出只能當線索，不能當事實）")
	)
	flag.Parse()
	if *zPath == "" {
		fmt.Fprintln(os.Stderr, "要指定 -z <執行檔>。本工具不含任何原版素材。")
		os.Exit(2)
	}

	data, err := os.ReadFile(*zPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	im, err := human68k.ParseZ(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("載入 %s\n", *zPath)
	fmt.Printf("  基底 0x%06X  text %d  data %d  bss %d  bss 結束 0x%06X\n\n",
		im.Base, im.TextSize, im.DataSize, im.BSSSize, im.BSSEnd())

	if *find != "" {
		pat, err := hex.DecodeString(strings.TrimSpace(*find))
		if err != nil || len(pat) == 0 {
			fmt.Fprintln(os.Stderr, "-find 要一串十六進位位元組")
			os.Exit(2)
		}
		n := 0
		for off := 0; off+len(pat) <= len(im.Body); off += 2 {
			if string(im.Body[off:off+len(pat)]) == string(pat) {
				fmt.Printf("  0x%06X\n", im.Base+uint32(off))
				n++
			}
		}
		fmt.Printf("共 %d 處（只掃字對齊的位置）\n", n)
		return
	}

	if *dumpAt != "" {
		as, ns, _ := strings.Cut(*dumpAt, ":")
		a, err1 := strconv.ParseUint(strings.TrimPrefix(as, "0x"), 16, 32)
		n, err2 := strconv.Atoi(ns)
		if err1 != nil || err2 != nil {
			fmt.Fprintln(os.Stderr, "-dump 的格式是 0x06A180:12")
			os.Exit(2)
		}
		dumpWords(im, uint32(a), n)
		return
	}

	if *scanFloat {
		scanFloatCalls(im)
		return
	}

	m, err := x68k.NewMachine(im, *ram)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
	if *keys != "" {
		m.Keys.PushString(strings.ReplaceAll(*keys, "\\n", "\n"))
	}
	if *randFixed >= 0 {
		m.RNG.Mode = x68k.RNGFixed
		m.RNG.Value = uint32(*randFixed)
	}
	if *disks != "" {
		for _, p := range strings.Split(*disks, ",") {
			d, err := x68k.LoadDIM(strings.TrimSpace(p))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			m.Drives = append(m.Drives, d)
		}
		fmt.Printf("軟碟機：%d 台\n\n", len(m.Drives))
	}
	if err := installStubs(m, *stub); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *stopIO != "" {
		m.Bus.StopOn = map[uint32]bool{}
		for _, item := range strings.Split(*stopIO, ",") {
			v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(item), "0x"), 16, 32)
			if err != nil {
				fmt.Fprintf(os.Stderr, "-stop-io %q 不是十六進位位址\n", item)
				os.Exit(2)
			}
			m.Bus.StopOn[uint32(v)] = true
		}
	}
	m.Bus.LatchIO = *latch
	m.Bus.StrictIO = !*lenient
	m.LenientServices = *lenient
	m.SetTraceDepth(*trace)
	if *hot > 0 {
		m.HotPC = map[uint32]int{}
	}
	var watchLog []string
	if *watch != "" {
		m.Bus.Watch = map[uint32]bool{}
		for _, item := range strings.Split(*watch, ",") {
			v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(item), "0x"), 16, 32)
			if err != nil {
				fmt.Fprintf(os.Stderr, "-watch %q 不是十六進位位址\n", item)
				os.Exit(2)
			}
			m.Bus.Watch[uint32(v)] = true
		}
		m.Bus.OnWatch = func(addr, v uint32, size int, pc uint32) {
			if len(watchLog) < *watchMax {
				watchLog = append(watchLog,
					fmt.Sprintf("0x%06X ← 0x%0*X（%d bytes）PC=0x%06X", addr, size*2, v, size, pc))
			}
		}
	}
	var svcLog []string
	if *logSvc > 0 {
		m.ServiceLog = func(line string) {
			svcLog = append(svcLog, line)
			if len(svcLog) > *logSvc {
				svcLog = svcLog[1:]
			}
		}
	}

	var stopErr error
	for m.Steps() < *maxSteps {
		if err := m.Step(); err != nil {
			stopErr = err
			break
		}
	}

	fmt.Printf("執行 %d 道指令後停下（%d 週期，垂直同步 %d 次，DMA 完成 %d 次）\n",
		m.Steps(), m.Cycles(), m.VDispCalls, m.DMACTransfers)
	if stopErr != nil {
		fmt.Printf("停下的原因：%v\n", stopErr)
	} else {
		fmt.Printf("停下的原因：跑滿 -steps=%d\n", *maxSteps)
	}

	if *lenient {
		fmt.Println("\n⚠ -lenient：沒實作的一律回 0。程式會依回傳值分支，" +
			"所以以下清單只能當「還有哪些服務存在」的線索，不能當事實——" +
			"可能少（走錯路沒碰到），也可能多（走進正常情況不會進的錯誤處理）。")
	}

	if len(watchLog) > 0 {
		fmt.Printf("\n== 監看到的寫入（前 %d 筆）\n", len(watchLog))
		for _, l := range watchLog {
			fmt.Println("  " + l)
		}
	}

	if *hot > 0 {
		type pc struct {
			addr uint32
			n    int
		}
		var list []pc
		for a, n := range m.HotPC {
			list = append(list, pc{a, n})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
		if len(list) > *hot {
			list = list[:*hot]
		}
		fmt.Printf("\n== 走過 %d 個相異位址；執行次數最多的 %d 個\n", len(m.HotPC), len(list))
		for _, e := range list {
			fmt.Printf("  0x%06X  x%d\n", e.addr, e.n)
		}
	}

	if *shot != "" {
		n := m.Bus.TextNonZero(*shotW, *shotH)
		f, err := os.Create(*shot)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := png.Encode(f, m.Bus.TextImage(*shotW, *shotH)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		f.Close()
		fmt.Printf("\n截圖：%s（%d×%d，非 0 像素 %d）\n", *shot, *shotW, *shotH, n)
	}

	if len(svcLog) > 0 {
		fmt.Printf("\n== 最後 %d 次服務呼叫\n", len(svcLog))
		for _, l := range svcLog {
			fmt.Println("  " + l)
		}
	}

	if tp := m.Trace(); len(tp) > 0 {
		fmt.Printf("\n== 停下來之前的最後 %d 道指令\n", len(tp))
		for _, t := range tp {
			fmt.Printf("  0x%06X  %04X %04X %04X\n", t.PC, t.Words[0], t.Words[1], t.Words[2])
		}
		st := m.CPU.State
		fmt.Println("== 暫存器")
		for i := 0; i < 8; i++ {
			fmt.Printf("  D%d = 0x%08X\n", i, st.D[i])
		}
		for i := 0; i < 7; i++ {
			fmt.Printf("  A%d = 0x%08X\n", i, st.A[i])
		}
		fmt.Printf("  USP = 0x%08X  SSP = 0x%08X  SR = 0x%04X\n", st.USP, st.SSP, st.SR)
	}

	if m.RNG != nil && (len(m.RNG.Log) > 0 || len(m.RNG.Seeds) > 0) {
		fmt.Printf("\n== 亂數：srand %d 次%v，rand %d 次\n",
			len(m.RNG.Seeds), m.RNG.Seeds, len(m.RNG.Log))
	}

	if len(m.Opens) > 0 {
		fmt.Printf("\n== 開過的檔案（%d 次）\n", len(m.Opens))
		for _, o := range m.Opens {
			fmt.Printf("  %s\n", o)
		}
	}

	svcs := m.SortedServices()
	fmt.Printf("\n== 服務呼叫（%d 種）\n", len(svcs))
	for _, s := range svcs {
		name := s.Name
		if name != "" {
			name = " " + name
		}
		mark := ""
		if s.Stubbed {
			mark = "  ⚠回0混過去"
		}
		fmt.Printf("  %-9s $%02X%-10s x%-5d 第一次 PC=0x%06X%s\n",
			s.Kind, s.Number, name, s.Count, s.FirstPC, mark)
	}
	if len(svcs) == 0 {
		fmt.Println("  （還沒走到任何一個）")
	}

	// 依區塊彙總。逐位址列出來對暫存器有用（就那幾個），對 VRAM 沒有用
	// ——清一次畫面就是六萬多筆，把真正該看的東西淹掉。
	type agg struct {
		region   x68k.Region
		reads    int
		writes   int
		addrs    map[uint32]bool
		firstPC  uint32
		lo, hi   uint32
	}
	var regions []*agg
	byRegion := map[x68k.Region]*agg{}
	for _, ac := range m.Bus.IO() {
		g, ok := byRegion[ac.Region]
		if !ok {
			g = &agg{region: ac.Region, addrs: map[uint32]bool{},
				firstPC: ac.PC, lo: ac.Address, hi: ac.Address}
			byRegion[ac.Region] = g
			regions = append(regions, g)
		}
		if ac.Write {
			g.writes += ac.Count
		} else {
			g.reads += ac.Count
		}
		g.addrs[ac.Address] = true
		if ac.Address < g.lo {
			g.lo = ac.Address
		}
		if ac.Address > g.hi {
			g.hi = ac.Address
		}
	}
	fmt.Printf("\n== 主記憶體以外的存取（%d 區）\n", len(regions))
	for _, g := range regions {
		fmt.Printf("  %-24s 讀 %-9d 寫 %-9d 相異位址 %-6d 0x%06X–0x%06X 第一次 PC=0x%06X\n",
			g.region, g.reads, g.writes, len(g.addrs), g.lo, g.hi, g.firstPC)
		if len(g.addrs) <= 16 {
			var list []uint32
			for a := range g.addrs {
				list = append(list, a)
			}
			sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
			for _, a := range list {
				fmt.Printf("      0x%06X\n", a)
			}
		}
	}
	if len(regions) == 0 {
		fmt.Println("  （還沒碰到）")
	}

	if stopErr != nil {
		os.Exit(3)
	}
}
