package psd

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"io"
)

// @region psd:decode -- CHANNEL DECODING

// caps on decoded image sizes.
const (
	maxDim    = 1 << 18
	maxPixels = 1 << 30
)

func checkSize(w, h int) error {
	if w > maxDim || h > maxDim || w*h > maxPixels {
		return fmt.Errorf("%dx%d exceeds size limit", w, h)
	}
	return nil
}

// decodes one channel plane of w*h bytes.
func decodePlane(data []byte, w, h int) ([]byte, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("channel too short")
	}
	comp := binary.BigEndian.Uint16(data)
	return decodeBody(comp, data[2:], w, h)
}

func decodeBody(comp uint16, body []byte, w, h int) ([]byte, error) {
	if err := checkSize(w, h); err != nil {
		return nil, err
	}
	n := w * h
	switch comp {
	case 0:
		if len(body) < n {
			return nil, fmt.Errorf("raw channel short")
		}
		return body[:n], nil
	case 1:
		if len(body) < 2*h {
			return nil, fmt.Errorf("rle counts short")
		}
		out := make([]byte, 0, n)
		p := 2 * h
		for row := range h {
			rl := int(binary.BigEndian.Uint16(body[2*row:]))
			if p+rl > len(body) {
				return nil, fmt.Errorf("rle row overflows")
			}
			out = unpackBits(out, body[p:p+rl], w)
			p += rl
		}
		return out, nil
	case 2, 3:
		zr, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out := make([]byte, n)
		if _, err := io.ReadFull(zr, out); err != nil {
			return nil, err
		}
		if comp == 3 {
			for y := range h {
				row := out[y*w : (y+1)*w]
				for x := 1; x < w; x++ {
					row[x] += row[x-1]
				}
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown compression %d", comp)
}

// appends exactly w decoded bytes (padding/truncating malformed rows).
func unpackBits(out, src []byte, w int) []byte {
	start := len(out)
	for i := 0; i < len(src) && len(out)-start < w; {
		c := int(int8(src[i]))
		i++
		switch {
		case c >= 0:
			end := min(i+c+1, len(src))
			out = append(out, src[i:end]...)
			i = end
		case c > -128:
			if i < len(src) {
				for range 1 - c {
					out = append(out, src[i])
				}
				i++
			}
		}
	}
	for len(out)-start < w {
		out = append(out, 0)
	}
	return out[:start+w]
}

// @region psd:image -- LAYER + COMPOSITE IMAGES

type maskInfo struct {
	rect     image.Rectangle
	defColor byte
	disabled bool
}

func (r *Record) maskInfo() (maskInfo, bool) {
	if len(r.Mask) < 18 {
		return maskInfo{}, false
	}
	m := r.Mask
	i32 := func(o int) int { return int(int32(binary.BigEndian.Uint32(m[o:]))) }
	return maskInfo{
		rect:     image.Rect(i32(4), i32(0), i32(12), i32(8)),
		defColor: m[16],
		disabled: m[17]&0x02 != 0,
	}, true
}

func (r *Record) Bounds() image.Rectangle {
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom))
}

// decodes a layer's pixels in canvas coordinates, with its mask applied.
// Returns nil for empty layers.
func (f *File) LayerImage(r *Record) (*image.NRGBA, error) {
	b := r.Bounds()
	if b.Empty() {
		return nil, nil
	}
	w, h := b.Dx(), b.Dy()
	if err := checkSize(w, h); err != nil {
		return nil, err
	}
	gray := f.Header.Mode == ModeGrayscale
	colorPlanes := int16(3)
	if gray {
		colorPlanes = 1
	}
	planes := map[int16][]byte{}
	var mask []byte
	mi, hasMask := r.maskInfo()
	for _, ch := range r.Channels {
		if ch.ID == -2 {
			if hasMask && !mi.disabled && !mi.rect.Empty() {
				p, err := decodePlane(ch.Data, mi.rect.Dx(), mi.rect.Dy())
				if err != nil {
					return nil, fmt.Errorf("mask: %w", err)
				}
				mask = p
			}
			continue
		}
		if ch.ID < -1 || ch.ID >= colorPlanes {
			continue
		}
		p, err := decodePlane(ch.Data, w, h)
		if err != nil {
			return nil, fmt.Errorf("channel %d: %w", ch.ID, err)
		}
		planes[ch.ID] = p
	}
	img := image.NewNRGBA(b)
	for i := range w * h {
		o := i * 4
		if gray {
			v := at(planes[0], i, 0)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2] = v, v, v
		} else {
			img.Pix[o] = at(planes[0], i, 0)
			img.Pix[o+1] = at(planes[1], i, 0)
			img.Pix[o+2] = at(planes[2], i, 0)
		}
		img.Pix[o+3] = at(planes[-1], i, 255)
	}
	if mask != nil {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				mv := mi.defColor
				if (image.Point{x, y}).In(mi.rect) {
					mv = mask[(y-mi.rect.Min.Y)*mi.rect.Dx()+(x-mi.rect.Min.X)]
				}
				o := img.PixOffset(x, y) + 3
				img.Pix[o] = byte(int(img.Pix[o]) * int(mv) / 255)
			}
		}
	}
	return img, nil
}

func at(p []byte, i int, def byte) byte {
	if p == nil || i >= len(p) {
		return def
	}
	return p[i]
}

// decodes the flattened composite stored in the file.
func (f *File) CompositeImage() (*image.NRGBA, error) {
	if len(f.Composite) < 2 {
		return nil, fmt.Errorf("no composite image")
	}
	w, h := int(f.Header.Width), int(f.Header.Height)
	nc := int(f.Header.Channels)
	gray := f.Header.Mode == ModeGrayscale
	colorPlanes := 3
	if gray {
		colorPlanes = 1
	}
	if nc < colorPlanes {
		return nil, fmt.Errorf("composite has %d channels, need %d", nc, colorPlanes)
	}
	if err := checkSize(w, h); err != nil {
		return nil, err
	}
	// in layered files, extra channels are saved selections unless flagged as merged transparency
	need := colorPlanes
	if nc > colorPlanes && f.MergedAlpha {
		need++
	}
	comp := binary.BigEndian.Uint16(f.Composite)
	body := f.Composite[2:]
	planes := make([][]byte, need)
	switch comp {
	case 0:
		for c := range need {
			if len(body) < (c+1)*w*h {
				return nil, fmt.Errorf("composite short")
			}
			planes[c] = body[c*w*h : (c+1)*w*h]
		}
	case 1:
		if len(body) < 2*h*nc {
			return nil, fmt.Errorf("composite rle short")
		}
		p := 2 * h * nc
		for c := range need {
			out := make([]byte, 0, w*h)
			for row := range h {
				rl := int(binary.BigEndian.Uint16(body[2*(c*h+row):]))
				if p+rl > len(body) {
					return nil, fmt.Errorf("composite rle overflow")
				}
				out = unpackBits(out, body[p:p+rl], w)
				p += rl
			}
			planes[c] = out
		}
	default:
		return nil, fmt.Errorf("unsupported composite compression %d", comp)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	var alpha []byte
	if need > colorPlanes {
		alpha = planes[colorPlanes]
	}
	for i := range w * h {
		o := i * 4
		if gray {
			v := planes[0][i]
			img.Pix[o], img.Pix[o+1], img.Pix[o+2] = v, v, v
		} else {
			img.Pix[o], img.Pix[o+1], img.Pix[o+2] = planes[0][i], planes[1][i], planes[2][i]
		}
		img.Pix[o+3] = at(alpha, i, 255)
	}
	if alpha != nil {
		unmatte(img)
	}
	return img, nil
}

// the merged image is matted against white when it has transparency.
func unmatte(img *image.NRGBA) {
	for o := 0; o < len(img.Pix); o += 4 {
		a := int(img.Pix[o+3])
		if a == 0 || a == 255 {
			continue
		}
		for c := range 3 {
			v := (int(img.Pix[o+c]) - (255 - a)) * 255 / a
			img.Pix[o+c] = byte(max(0, min(255, v)))
		}
	}
}

// @region psd:composite -- NAIVE COMPOSITING

// naive render of n: normal blending, opacity and masks only. Hidden
// descendants are skipped; n itself renders even when hidden. Special layers
// render whatever raster data they carry. Returns nil when nothing is drawn.
func (d *Doc) Render(n *Node) (*image.NRGBA, error) {
	var layers []*Node
	var opac []float64
	var collect func(c *Node, o float64)
	collect = func(c *Node, o float64) {
		o *= float64(c.Rec.Opacity) / 255
		if !c.IsGroup() {
			layers = append(layers, c)
			opac = append(opac, o)
			return
		}
		for i := len(c.Children) - 1; i >= 0; i-- {
			if ch := c.Children[i]; !ch.Hidden() {
				collect(ch, o)
			}
		}
	}
	if n.IsRoot() {
		for i := len(n.Children) - 1; i >= 0; i-- {
			if ch := n.Children[i]; !ch.Hidden() {
				collect(ch, 1)
			}
		}
	} else {
		collect(n, 1)
	}

	var bounds image.Rectangle
	for _, l := range layers {
		bounds = bounds.Union(l.Rec.Bounds())
	}
	if n.IsRoot() {
		bounds = image.Rect(0, 0, int(d.File.Header.Width), int(d.File.Header.Height))
	}
	if bounds.Empty() {
		return nil, nil
	}
	if err := checkSize(bounds.Dx(), bounds.Dy()); err != nil {
		return nil, err
	}
	out := image.NewNRGBA(bounds)
	for i, l := range layers {
		img, err := d.File.LayerImage(l.Rec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.Name(), err)
		}
		if img != nil {
			over(out, img, int(opac[i]*255+0.5))
		}
	}
	return out, nil
}

// src-over of src onto dst (both non-premultiplied) with extra opacity 0..255.
func over(dst, src *image.NRGBA, opacity int) {
	r := dst.Bounds().Intersect(src.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		so := src.PixOffset(r.Min.X, y)
		do := dst.PixOffset(r.Min.X, y)
		for x := r.Min.X; x < r.Max.X; x, so, do = x+1, so+4, do+4 {
			sa := int(src.Pix[so+3]) * opacity / 255
			if sa == 0 {
				continue
			}
			da := int(dst.Pix[do+3])
			oa := sa + da*(255-sa)/255
			for c := range 3 {
				sc, dc := int(src.Pix[so+c]), int(dst.Pix[do+c])
				dst.Pix[do+c] = byte((sc*sa + dc*da*(255-sa)/255) / oa)
			}
			dst.Pix[do+3] = byte(oa)
		}
	}
}

// whole-document image prefers the stored composite until the session edits
// anything (it is stale afterwards); everything else is a naive render.
func (d *Doc) Image(n *Node) (*image.NRGBA, error) {
	if n.IsRoot() && !d.Edited() {
		if img, err := d.File.CompositeImage(); err == nil {
			return img, nil
		}
	}
	return d.Render(n)
}
