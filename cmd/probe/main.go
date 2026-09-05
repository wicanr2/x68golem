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
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/wicanr2/x68golem/internal/human68k"
	"github.com/wicanr2/x68golem/internal/x68k"
)

func main() {
	var (
		zPath    = flag.String("z", "", "Human68k `.Z` 執行檔（玩家自備）")
		maxSteps = flag.Uint64("steps", 50_000_000, "最多執行幾道指令")
		ram      = flag.Int("ram", x68k.DefaultRAMSize, "主記憶體大小（bytes）")
		trace = flag.Int("trace", 0, "停下來時印出最後 N 道指令與暫存器")
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

	m, err := x68k.NewMachine(im, *ram)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m.Bus.StrictIO = !*lenient
	m.LenientServices = *lenient
	m.SetTraceDepth(*trace)

	var stopErr error
	for m.Steps() < *maxSteps {
		if err := m.Step(); err != nil {
			stopErr = err
			break
		}
	}

	fmt.Printf("執行 %d 道指令後停下\n", m.Steps())
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

	if tp := m.Trace(); len(tp) > 0 {
		fmt.Printf("\n== 停下來之前的最後 %d 道指令\n", len(tp))
		for _, t := range tp {
			fmt.Printf("  0x%06X  %04X\n", t.PC, t.Opcode)
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
