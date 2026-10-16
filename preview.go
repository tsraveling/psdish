package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/tsraveling/psdish/psd"
	"golang.org/x/sys/unix"
)

// @region preview:output -- SHARED TERMINAL WRITER

// stdout wrapper shared with bubbletea so image escapes never interleave with
// a frame (the renderer writes each frame in a single Write).
type lockedOutput struct {
	mu sync.Mutex
	f  *os.File
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.f.Write(p)
}

func (o *lockedOutput) Read(p []byte) (int, error) { return o.f.Read(p) }
func (o *lockedOutput) Close() error               { return o.f.Close() }
func (o *lockedOutput) Fd() uintptr                { return o.f.Fd() }

// @region preview:mode -- GRAPHICS DETECTION

type graphicsMode int

const (
	graphicsBlocks graphicsMode = iota
	graphicsKitty
)

func detectGraphics() graphicsMode {
	switch os.Getenv("PSDISH_GRAPHICS") {
	case "kitty":
		return graphicsKitty
	case "blocks":
		return graphicsBlocks
	}
	if os.Getenv("TMUX") != "" {
		return graphicsBlocks
	}
	term := os.Getenv("TERM")
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	if strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") || tp == "ghostty" || os.Getenv("KITTY_WINDOW_ID") != "" {
		return graphicsKitty
	}
	return graphicsBlocks
}

// cell size in pixels; falls back to a typical 1:2 cell.
func cellSize(f *os.File) (int, int) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 10, 20
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}

// @region preview:state -- REQUEST + CACHE

type previewKey struct {
	target int // node ID; 0 = whole document
	rev    int
	w, h   int
}

type previewContent struct {
	key     previewKey
	lines   []string // image rows, already centered in the pane
	message string   // shown centered in gray when there is no image
	caption string
	approx  bool
	imageID uint32
}

type previewer struct {
	mode         graphicsMode
	out          *lockedOutput
	cellW, cellH int

	nextID  uint32
	seq     int
	pending previewKey
	current *previewContent
	cache   []*previewContent // most recent last
}

const (
	previewCacheSize = 12
	debounce         = 60 * time.Millisecond
	deleteDelay      = 200 * time.Millisecond
)

func newPreviewer(out *lockedOutput, mode graphicsMode) *previewer {
	cw, ch := 10, 20
	if out != nil {
		cw, ch = cellSize(out.f)
	}
	// random base keeps our IDs clear of other programs in the same window
	return &previewer{mode: mode, out: out, cellW: cw, cellH: ch, nextID: 1 + rand.Uint32N(1<<23)}
}

type previewTickMsg struct {
	seq int
	key previewKey
}

type previewDoneMsg struct{ content *previewContent }

type previewDeleteMsg struct{ id uint32 }

func (p *previewer) cached(k previewKey) *previewContent {
	for _, c := range p.cache {
		if c.key == k {
			return c
		}
	}
	return nil
}

// schedules a debounced render when the wanted preview changed.
func (p *previewer) request(k previewKey) tea.Cmd {
	if p.current != nil && p.current.key == k {
		return nil
	}
	if c := p.cached(k); c != nil {
		p.current = c
		return nil
	}
	if p.pending == k {
		return nil
	}
	p.pending = k
	p.seq++
	seq := p.seq
	return tea.Tick(debounce, func(time.Time) tea.Msg { return previewTickMsg{seq: seq, key: k} })
}

// called on tick; starts the render if the request is still current.
func (p *previewer) start(msg previewTickMsg, doc *psd.Doc) tea.Cmd {
	if msg.seq != p.seq {
		return nil
	}
	snap := doc.Snapshot()
	id := uint32(0)
	if p.mode == graphicsKitty {
		id = p.nextID
		p.nextID++
	}
	// copy so the render goroutine never reads fields Update keeps writing
	pv := *p
	return func() tea.Msg {
		return previewDoneMsg{content: pv.render(msg.key, snap, id)}
	}
}

// stores a finished render; returns a cmd that frees any evicted image.
func (p *previewer) finish(c *previewContent) tea.Cmd {
	if c.key == p.pending {
		p.pending = previewKey{}
	}
	p.cache = append(p.cache, c)
	var cmds []tea.Cmd
	for len(p.cache) > previewCacheSize {
		// never evict the on-screen image
		i := 0
		if p.cache[0] == p.current {
			i = 1
		}
		old := p.cache[i]
		p.cache = slices.Delete(p.cache, i, i+1)
		if old.imageID != 0 {
			cmds = append(cmds, p.deleteLater(old.imageID))
		}
	}
	return tea.Batch(cmds...)
}

func (p *previewer) deleteLater(id uint32) tea.Cmd {
	return tea.Tick(deleteDelay, func(time.Time) tea.Msg { return previewDeleteMsg{id: id} })
}

func (p *previewer) deleteImage(id uint32) {
	if p.out != nil && id != 0 {
		fmt.Fprintf(p.out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
	}
}

// frees every image we transmitted.
func (p *previewer) cleanup() {
	for _, c := range p.cache {
		p.deleteImage(c.imageID)
	}
	if p.current != nil && !slices.Contains(p.cache, p.current) {
		p.deleteImage(p.current.imageID)
	}
	p.cache = nil
}

// @region preview:render -- IMAGE → TERMINAL CELLS

func (p *previewer) render(k previewKey, snap *psd.Doc, id uint32) *previewContent {
	c := &previewContent{key: k}
	var n *psd.Node
	if k.target == 0 {
		n = snap.Root
	} else if n = snap.Find(k.target); n == nil {
		c.message = "nothing selected"
		return c
	}
	if s := n.Special(); s != "" {
		c.message = fmt.Sprintf("no preview available for %s layers.", s)
		return c
	}
	c.approx = n.ContainsSpecial()

	img, err := snap.Image(n)
	switch {
	case err != nil:
		c.message = "preview failed: " + displayName(err.Error())
		return c
	case img == nil:
		c.message = "nothing visible to preview"
		return c
	}
	b := img.Bounds()
	c.caption = fmt.Sprintf("%d×%d", b.Dx(), b.Dy())
	if k.w < 1 || k.h < 1 {
		return c
	}
	if p.mode == graphicsKitty {
		c.lines, c.imageID = p.kittyLines(img, k.w, k.h, id)
	} else {
		c.lines = p.blockLines(img, k.w, k.h)
	}
	return c
}

func (p *previewer) kittyLines(img *image.NRGBA, w, h int, id uint32) ([]string, uint32) {
	b := img.Bounds()
	s := math.Min(float64(w*p.cellW)/float64(b.Dx()), float64(h*p.cellH)/float64(b.Dy()))
	tw := max(1, int(math.Round(float64(b.Dx())*s)))
	th := max(1, int(math.Round(float64(b.Dy())*s)))
	cols := min(w, max(1, (tw+p.cellW-1)/p.cellW))
	rows := min(h, max(1, (th+p.cellH-1)/p.cellH))
	src := img
	if s < 1 {
		src = downscale(img, tw, th)
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, src); err != nil {
		return nil, 0
	}
	p.transmit(id, buf.Bytes(), cols, rows)

	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	left := strings.Repeat(" ", (w-cols)/2)
	lines := make([]string, 0, h)
	top := (h - rows) / 2
	for range top {
		lines = append(lines, "")
	}
	for r := range rows {
		var sb strings.Builder
		sb.WriteString(left)
		sb.WriteString(color)
		for c := range cols {
			sb.WriteRune(kitty.Placeholder)
			sb.WriteRune(kitty.Diacritic(r))
			sb.WriteRune(kitty.Diacritic(c))
		}
		sb.WriteString("\x1b[39m")
		lines = append(lines, sb.String())
	}
	return lines, id
}

// sends PNG data as a virtual placement for unicode placeholders.
func (p *previewer) transmit(id uint32, data []byte, cols, rows int) {
	if p.out == nil {
		return
	}
	enc := base64.StdEncoding.EncodeToString(data)
	var sb strings.Builder
	for i := 0; i < len(enc); i += kitty.MaxChunkSize {
		end := min(i+kitty.MaxChunkSize, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&sb, "\x1b_Ga=T,U=1,f=100,t=d,q=2,i=%d,c=%d,r=%d,m=%d;", id, cols, rows, more)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;", more)
		}
		sb.WriteString(enc[i:end])
		sb.WriteString("\x1b\\")
	}
	p.out.Write([]byte(sb.String()))
}

// half-block fallback: each cell shows two vertically stacked pixels.
func (p *previewer) blockLines(img *image.NRGBA, w, h int) []string {
	b := img.Bounds()
	pxW, pxH := float64(p.cellW), float64(p.cellH)/2
	s := math.Min(float64(w)*pxW/float64(b.Dx()), float64(h*2)*pxH/float64(b.Dy()))
	cols := max(1, min(w, int(math.Round(float64(b.Dx())*s/pxW))))
	half := max(1, min(h*2, int(math.Round(float64(b.Dy())*s/pxH))))
	if half%2 == 1 {
		half++
	}
	small := downscale(img, cols, half)
	bg := previewBgColor
	px := func(x, y int) [3]int {
		o := small.PixOffset(x, y)
		a := int(small.Pix[o+3])
		var c [3]int
		for i := range 3 {
			c[i] = (int(small.Pix[o+i])*a + bg[i]*(255-a)) / 255
		}
		return c
	}
	left := strings.Repeat(" ", (w-cols)/2)
	rows := half / 2
	lines := make([]string, 0, h)
	for range (h - rows) / 2 {
		lines = append(lines, "")
	}
	for r := range rows {
		var sb strings.Builder
		sb.WriteString(left)
		for x := range cols {
			t, u := px(x, 2*r), px(x, 2*r+1)
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", t[0], t[1], t[2], u[0], u[1], u[2])
		}
		sb.WriteString("\x1b[0m")
		lines = append(lines, sb.String())
	}
	return lines
}

// box-filter resample to w×h (alpha-weighted); output origin is 0,0.
func downscale(src *image.NRGBA, w, h int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	sw, sh := b.Dx(), b.Dy()
	for y := range h {
		y0 := b.Min.Y + y*sh/h
		y1 := max(y0+1, b.Min.Y+(y+1)*sh/h)
		for x := range w {
			x0 := b.Min.X + x*sw/w
			x1 := max(x0+1, b.Min.X+(x+1)*sw/w)
			var r, g, bl, a, n int
			for yy := y0; yy < y1; yy++ {
				o := src.PixOffset(x0, yy)
				for xx := x0; xx < x1; xx, o = xx+1, o+4 {
					pa := int(src.Pix[o+3])
					r += int(src.Pix[o]) * pa
					g += int(src.Pix[o+1]) * pa
					bl += int(src.Pix[o+2]) * pa
					a += pa
					n++
				}
			}
			o := dst.PixOffset(x, y)
			if a > 0 {
				dst.Pix[o] = byte(r / a)
				dst.Pix[o+1] = byte(g / a)
				dst.Pix[o+2] = byte(bl / a)
				dst.Pix[o+3] = byte(a / n)
			}
		}
	}
	return dst
}
