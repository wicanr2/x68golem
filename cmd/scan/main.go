// scan 在一個原始檔案裡找位元組樣式。
//
//	tools/go.sh run ./cmd/scan -f FLOAT2.X -pat 41C64E6D,00343FD5,0019660D
//
// 用途之一：`FLOAT2.X` 的 `rand()` 演算法還沒解出來（`docs/spec/005` §4），
// 而常見的線性同餘產生器有幾組很好認的乘數。找到一個就把範圍縮到那一段，
// **找不到也是結論**——表示它不是那幾種現成的。
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	file := flag.String("f", "", "要掃的檔案")
	pats := flag.String("pat", "", "位元組樣式，十六進位、逗號分隔")
	ctx := flag.Int("ctx", 16, "命中時前後各印幾個 byte")
	flag.Parse()
	if *file == "" || *pats == "" {
		fmt.Fprintln(os.Stderr, "要指定 -f 與 -pat")
		os.Exit(2)
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s：%d bytes\n", *file, len(data))
	for _, p := range strings.Split(*pats, ",") {
		p = strings.TrimSpace(p)
		b, err := hex.DecodeString(p)
		if err != nil || len(b) == 0 {
			fmt.Fprintf(os.Stderr, "%q 不是十六進位位元組\n", p)
			os.Exit(2)
		}
		n := 0
		for i := 0; i+len(b) <= len(data); i++ {
			if string(data[i:i+len(b)]) != string(b) {
				continue
			}
			n++
			lo := i - *ctx
			if lo < 0 {
				lo = 0
			}
			hi := i + len(b) + *ctx
			if hi > len(data) {
				hi = len(data)
			}
			fmt.Printf("  %s 在 0x%X：%s\n", p, i, hex.EncodeToString(data[lo:hi]))
		}
		if n == 0 {
			fmt.Printf("  %s：沒有\n", p)
		}
	}
}
