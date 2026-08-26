// Tracing: converts a PNG (colored shapes on a white background) into ordered
// lists of pixel coordinates that follow the outline of each shape.
package photototrace

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
)

// ---------------------------------------------------------------- mask

// Mask is a binary image: true = ink (part of a shape), false = background.
type Mask struct {
	W, H int
	Px   []bool
}

func (m *Mask) At(x, y int) bool {
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return false // outside the image counts as background
	}
	return m.Px[y*m.W+x]
}

func (m *Mask) Set(x, y int, v bool) { m.Px[y*m.W+x] = v }

// Binarize marks a pixel as ink when it is far enough from white.
// thresh is 0..255: how much darker/more saturated than white a pixel must be.
// Fully or mostly transparent pixels count as background.
func Binarize(img image.Image, thresh int) *Mask {
	b := img.Bounds()
	m := &Mask{W: b.Dx(), H: b.Dy(), Px: make([]bool, b.Dx()*b.Dy())}
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA() // 16-bit, alpha-premultiplied
			if a < 0x8000 {
				continue
			}
			// un-premultiply and drop to 8-bit
			r8 := int(r * 0xffff / a >> 8)
			g8 := int(g * 0xffff / a >> 8)
			b8 := int(bl * 0xffff / a >> 8)
			// distance from white along the "most different" channel
			d := 255 - min3(r8, g8, b8)
			if d >= thresh {
				m.Set(x, y, true)
			}
		}
	}
	return m
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// ---------------------------------------------------------------- tracing

// 8 neighbours in clockwise order (screen coords: +y is down).
var dirs = [8]image.Point{
	{0, -1},  // N
	{1, -1},  // NE
	{1, 0},   // E
	{1, 1},   // SE
	{0, 1},   // S
	{-1, 1},  // SW
	{-1, 0},  // W
	{-1, -1}, // NW
}

func dirIndex(d image.Point) int {
	for i, v := range dirs {
		if v == d {
			return i
		}
	}
	return -1
}

// traceContour walks the outer border of the blob containing (start), using
// Moore-neighbour tracing. It returns the boundary pixels in order.
func traceContour(m *Mask, start image.Point) []image.Point {
	var contour []image.Point
	b := start
	back := 6 // we arrived from the west, which raster order guarantees is background
	seen := map[[3]int]bool{}

	for {
		key := [3]int{b.X, b.Y, back}
		if seen[key] {
			break // returned to a state we've already been in: the loop is closed
		}
		seen[key] = true
		contour = append(contour, b)

		found := false
		for i := 1; i <= 8; i++ {
			d := dirs[(back+i)%8]
			n := image.Pt(b.X+d.X, b.Y+d.Y)
			if !m.At(n.X, n.Y) {
				continue
			}
			// the background cell we checked just before n becomes the new backtrack
			pd := dirs[(back+i-1+8)%8]
			prev := image.Pt(b.X+pd.X, b.Y+pd.Y)
			back = dirIndex(image.Pt(prev.X-n.X, prev.Y-n.Y))
			b = n
			found = true
			break
		}
		if !found {
			break // isolated single pixel
		}
	}
	return contour
}

// fillComponent marks every pixel of one 8-connected blob as visited.
func fillComponent(m *Mask, visited []bool, start image.Point) {
	stack := []image.Point{start}
	visited[start.Y*m.W+start.X] = true
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range dirs {
			n := image.Pt(p.X+d.X, p.Y+d.Y)
			if !m.At(n.X, n.Y) || visited[n.Y*m.W+n.X] {
				continue
			}
			visited[n.Y*m.W+n.X] = true
			stack = append(stack, n)
		}
	}
}

// Contours returns one ordered outline per connected shape.
func Contours(m *Mask, minLen int) [][]image.Point {
	visited := make([]bool, m.W*m.H)
	var out [][]image.Point
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			if !m.At(x, y) || visited[y*m.W+x] {
				continue
			}
			c := traceContour(m, image.Pt(x, y))
			fillComponent(m, visited, image.Pt(x, y))
			if len(c) >= minLen {
				out = append(out, c)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- simplify

// Simplify runs Ramer-Douglas-Peucker: drops points that are within eps
// pixels of the straight line they sit on.
func Simplify(pts []image.Point, eps float64) []image.Point {
	if len(pts) < 3 || eps <= 0 {
		return pts
	}
	first, last := pts[0], pts[len(pts)-1]
	maxD, idx := 0.0, 0
	for i := 1; i < len(pts)-1; i++ {
		if d := perpDist(pts[i], first, last); d > maxD {
			maxD, idx = d, i
		}
	}
	if maxD <= eps {
		return []image.Point{first, last}
	}
	left := Simplify(pts[:idx+1], eps)
	right := Simplify(pts[idx:], eps)
	return append(left[:len(left)-1], right...)
}

func perpDist(p, a, b image.Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	if dx == 0 && dy == 0 {
		return math.Hypot(float64(p.X-a.X), float64(p.Y-a.Y))
	}
	num := math.Abs(dy*float64(p.X-a.X) - dx*float64(p.Y-a.Y))
	return num / math.Hypot(dx, dy)
}

// ---------------------------------------------------------------- entry point

// TraceResult is the outcome of tracing one image.
type TraceResult struct {
	Width, Height int
	Contours      [][]image.Point
}

// TraceFile decodes the image at path and returns its shape outlines.
func TraceFile(path string, thresh, minLen int, eps float64) (*TraceResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	mask := Binarize(img, thresh)
	cs := Contours(mask, minLen)
	for i := range cs {
		cs[i] = Simplify(cs[i], eps)
	}
	return &TraceResult{Width: mask.W, Height: mask.H, Contours: cs}, nil
}

// ---------------------------------------------------------------- output

// WriteTrace writes the points as CSV: a "contour,x,y" header followed by one
// point per line, in draw order. The contour column marks where one shape's
// outline ends and the next begins.
func WriteTrace(w io.Writer, res *TraceResult) error {
	if _, err := fmt.Fprintln(w, "contour,x,y"); err != nil {
		return err
	}
	for i, c := range res.Contours {
		for _, p := range c {
			if _, err := fmt.Fprintf(w, "%d,%d,%d\n", i, p.X, p.Y); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteTraceFile writes the points to path as CSV, creating or truncating it.
func WriteTraceFile(path string, res *TraceResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := WriteTrace(f, res); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
