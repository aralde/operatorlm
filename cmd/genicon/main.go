// genicon renders the OperatorLM application icon as a multi-resolution .ico
// file (16/32/48/256) at assets/icon.ico. The output is consumed by `rsrc`
// to produce rsrc_windows_amd64.syso, which `go build` embeds automatically
// so Windows Explorer shows the icon on OperatorLM.exe.
//
// Run from the repo root:
//
//	go run ./cmd/genicon
//	go run github.com/akavel/rsrc@latest -ico assets/icon.ico -o rsrc_windows_amd64.syso -arch amd64
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var (
	primary = color.RGBA{R: 0x4f, G: 0x8a, B: 0xff, A: 0xff}
	bg      = color.RGBA{0, 0, 0, 0}
)

func main() {
	sizes := []int{16, 32, 48, 256}
	pngs := make([][]byte, len(sizes))
	for i, s := range sizes {
		pngs[i] = encodePNG(renderIcon(s))
	}
	ico := buildICO(sizes, pngs)

	out := filepath.Join("assets", "icon.ico")
	if err := os.MkdirAll("assets", 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(out, ico, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s (%d bytes, %d sizes)", out, len(ico), len(sizes))
}

// renderIcon draws a hub-and-spoke graph centered in an sxs RGBA image:
// one filled central hub, four outer nodes at the diagonals, connected
// by anti-aliased lines. Same visual language as the tray icon.
func renderIcon(s int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	// transparent background
	for i := range img.Pix {
		img.Pix[i] = 0
	}

	cx, cy := float64(s)/2, float64(s)/2
	// radii tuned visually
	hubR := float64(s) * 0.18
	nodeR := float64(s) * 0.11
	outer := float64(s) * 0.36
	lineW := math.Max(1, float64(s)*0.05)

	// four outer node positions (NW, NE, SE, SW)
	offsets := [][2]float64{
		{-outer, -outer},
		{outer, -outer},
		{outer, outer},
		{-outer, outer},
	}

	// spokes first so the nodes sit on top
	for _, o := range offsets {
		drawLine(img, cx, cy, cx+o[0], cy+o[1], lineW, primary)
	}
	// hub
	drawDisc(img, cx, cy, hubR, primary)
	// outer nodes
	for _, o := range offsets {
		drawDisc(img, cx+o[0], cy+o[1], nodeR, primary)
	}
	return img
}

// drawDisc fills a circle with simple super-sampled anti-aliasing.
func drawDisc(img *image.RGBA, cx, cy, r float64, col color.RGBA) {
	x0 := int(math.Floor(cx - r - 1))
	y0 := int(math.Floor(cy - r - 1))
	x1 := int(math.Ceil(cx + r + 1))
	y1 := int(math.Ceil(cy + r + 1))
	b := img.Bounds()
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X-1 {
		x1 = b.Max.X - 1
	}
	if y1 > b.Max.Y-1 {
		y1 = b.Max.Y - 1
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			a := coverageCircle(float64(x), float64(y), cx, cy, r)
			if a > 0 {
				blend(img, x, y, col, a)
			}
		}
	}
}

// drawLine rasterises a thick line as a swept disc (cheap, looks fine at
// icon resolutions).
func drawLine(img *image.RGBA, x0, y0, x1, y1, width float64, col color.RGBA) {
	r := width / 2
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)
	if length == 0 {
		drawDisc(img, x0, y0, r, col)
		return
	}
	steps := int(math.Ceil(length * 2))
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		drawDisc(img, x0+dx*t, y0+dy*t, r, col)
	}
}

// coverageCircle returns approximate pixel coverage of a circle using 4x4
// super-sampling — cheap AA that looks clean at 16px and great at 256px.
func coverageCircle(px, py, cx, cy, r float64) float64 {
	const n = 4
	hit := 0
	r2 := r * r
	for sy := 0; sy < n; sy++ {
		for sx := 0; sx < n; sx++ {
			x := px + (float64(sx)+0.5)/n
			y := py + (float64(sy)+0.5)/n
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r2 {
				hit++
			}
		}
	}
	return float64(hit) / float64(n*n)
}

func blend(img *image.RGBA, x, y int, col color.RGBA, a float64) {
	i := img.PixOffset(x, y)
	dst := img.Pix[i : i+4 : i+4]
	srcA := float64(col.A) / 255 * a
	invA := 1 - srcA
	dr := float64(dst[0]) / 255
	dg := float64(dst[1]) / 255
	db := float64(dst[2]) / 255
	da := float64(dst[3]) / 255
	outA := srcA + da*invA
	if outA <= 0 {
		return
	}
	outR := (float64(col.R)/255*srcA + dr*da*invA) / outA
	outG := (float64(col.G)/255*srcA + dg*da*invA) / outA
	outB := (float64(col.B)/255*srcA + db*da*invA) / outA
	dst[0] = uint8(outR * 255)
	dst[1] = uint8(outG * 255)
	dst[2] = uint8(outB * 255)
	dst[3] = uint8(outA * 255)
}

func encodePNG(img *image.RGBA) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}

// buildICO produces an .ico file holding one PNG-encoded entry per size.
// Vista+ supports PNG payloads inside ICO containers, which keeps the file
// small and preserves alpha — including the 256x256 entry Explorer uses for
// large icon views.
func buildICO(sizes []int, pngs [][]byte) []byte {
	var buf bytes.Buffer
	// ICONDIR
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(len(sizes)))

	headerSize := 6 + 16*len(sizes)
	offset := uint32(headerSize)
	for i, s := range sizes {
		d := byte(s)
		if s >= 256 {
			d = 0
		}
		buf.WriteByte(d)                                              // width
		buf.WriteByte(d)                                              // height
		buf.WriteByte(0)                                              // palette
		buf.WriteByte(0)                                              // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))            // planes
		binary.Write(&buf, binary.LittleEndian, uint16(32))           // bpp
		binary.Write(&buf, binary.LittleEndian, uint32(len(pngs[i]))) // size
		binary.Write(&buf, binary.LittleEndian, offset)               // offset
		offset += uint32(len(pngs[i]))
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes()
}
