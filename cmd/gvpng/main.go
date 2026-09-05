// gvpng 把一份 graphics VRAM dump 畫成 PNG，用**索引當灰階**。
//
//	tools/go.sh run ./cmd/gvpng -in dump.bin -out x.png
//
// 為什麼不套調色盤：對拍比的是索引（`docs/spec/003`），而兩邊的調色盤
// 未必同時 dump 到。把索引直接當灰階看，圖案對不對一眼就知道，
// 顏色對不對另外比。
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	in := flag.String("in", "", "graphics VRAM dump（512 KB）")
	out := flag.String("out", "", "輸出 PNG")
	w := flag.Int("w", 512, "寬")
	h := flag.Int("h", 512, "高")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "要指定 -in 與 -out")
		os.Exit(2)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	img := image.NewRGBA(image.Rect(0, 0, *w, *h))
	nz := 0
	for y := 0; y < *h; y++ {
		for x := 0; x < *w; x++ {
			off := (y**w + x) * 2
			if off+1 >= len(data) {
				continue
			}
			v := data[off+1] // 低 byte ＝ 256 色索引
			if v != 0 {
				nz++
			}
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s → %s（%d×%d，非 0 像素 %d）\n", *in, *out, *w, *h, nz)
}
