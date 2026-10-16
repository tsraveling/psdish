// Package psd reads and writes the structural parts of Photoshop documents:
// layer order, grouping, names and visibility. Pixel data is carried through
// untouched and only decoded on demand for previews and exports.
package psd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// @region psd:model -- FILE + RECORD TYPES

const (
	ModeGrayscale = 1
	ModeRGB       = 3
)

// layer flag bits
const (
	flagHidden = 0x02
)

// section divider types (lsct)
const (
	SectionNone   = 0
	SectionOpen   = 1
	SectionClosed = 2
	SectionEnd    = 3
)

type Header struct {
	Channels uint16
	Height   uint32
	Width    uint32
	Depth    uint16
	Mode     uint16
}

type Resource struct {
	ID   uint16
	Name []byte // raw pascal string incl. even padding
	Data []byte
}

type Channel struct {
	ID   int16
	Data []byte // compression word + image data
}

type Block struct {
	Sig  [4]byte
	Key  string
	Data []byte
}

// one entry in the flat layer list; groups are two records (opener + end divider).
type Record struct {
	Top, Left, Bottom, Right int32
	Channels                 []Channel
	BlendSig                 [4]byte
	BlendKey                 [4]byte
	Opacity                  byte
	Clipping                 byte
	Flags                    byte
	Filler                   byte
	Mask                     []byte // mask data body (without length)
	Ranges                   []byte // blending ranges body (without length)
	PascalName               []byte // raw pascal name incl. 4-byte padding
	Blocks                   []Block

	// per-layer values from layer-indexed image resources (1024, 1026, 1072)
	target       bool
	groupInfo    uint16
	groupEnabled byte
}

type File struct {
	Header    Header
	ColorData []byte
	Resources []Resource

	// negative layer count in file: first alpha channel holds merged transparency
	MergedAlpha bool
	Records     []*Record // file order: bottom-most first

	hasLayerSection bool
	layerPadAlign   int
	GlobalMask      []byte // body without length
	Extra           []byte // tagged blocks after global mask, raw to end of section
	Composite       []byte // image data section, raw
}

func (r *Record) Hidden() bool { return r.Flags&flagHidden != 0 }

func (r *Record) SetHidden(h bool) {
	if h {
		r.Flags |= flagHidden
	} else {
		r.Flags &^= flagHidden
	}
}

func (r *Record) block(key string) *Block {
	for i := range r.Blocks {
		if r.Blocks[i].Key == key {
			return &r.Blocks[i]
		}
	}
	return nil
}

func (r *Record) SectionType() int {
	b := r.block("lsct")
	if b == nil {
		b = r.block("lsdk")
	}
	if b == nil || len(b.Data) < 4 {
		return SectionNone
	}
	return int(binary.BigEndian.Uint32(b.Data))
}

func (r *Record) LayerID() (uint32, bool) {
	b := r.block("lyid")
	if b == nil || len(b.Data) < 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b.Data), true
}

// unicode name if present, else pascal name.
func (r *Record) Name() string {
	if b := r.block("luni"); b != nil && len(b.Data) >= 4 {
		n := int(binary.BigEndian.Uint32(b.Data))
		if 4+2*n <= len(b.Data) {
			u := make([]uint16, n)
			for i := range u {
				u[i] = binary.BigEndian.Uint16(b.Data[4+2*i:])
			}
			return strings.TrimRight(string(utf16.Decode(u)), "\x00")
		}
	}
	if len(r.PascalName) == 0 {
		return ""
	}
	n := int(r.PascalName[0])
	if 1+n > len(r.PascalName) {
		n = len(r.PascalName) - 1
	}
	return string(r.PascalName[1 : 1+n])
}

func (r *Record) SetName(name string) {
	r.PascalName = pascalName(name)
	data := encodeLuni(name)
	if b := r.block("luni"); b != nil {
		b.Data = data
		return
	}
	r.Blocks = append([]Block{{Sig: sig8BIM, Key: "luni", Data: data}}, r.Blocks...)
}

// deep enough copy for undo: slices that get replaced (not mutated) are shared.
func (r *Record) clone() *Record {
	c := *r
	c.Blocks = append([]Block(nil), r.Blocks...)
	return &c
}

var sig8BIM = [4]byte{'8', 'B', 'I', 'M'}

// @region psd:read -- PARSING

var ErrUnsupported = errors.New("unsupported PSD")

func Open(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

type reader struct {
	b   []byte
	pos int
	err error
}

// on error, returns zeros for fixed-size reads (<= 8 bytes) and nil otherwise.
func (r *reader) take(n int) []byte {
	if r.err == nil && (n < 0 || r.pos+n > len(r.b)) {
		r.err = io.ErrUnexpectedEOF
	}
	if r.err != nil {
		if n > 8 {
			return nil
		}
		return make([]byte, max(n, 0))
	}
	s := r.b[r.pos : r.pos+n]
	r.pos += n
	return s
}

func (r *reader) u8() byte     { return r.take(1)[0] }
func (r *reader) u16() uint16  { return binary.BigEndian.Uint16(r.take(2)) }
func (r *reader) u32() uint32  { return binary.BigEndian.Uint32(r.take(4)) }
func (r *reader) i32() int32   { return int32(r.u32()) }
func (r *reader) sig() [4]byte { return [4]byte(r.take(4)) }

func Parse(b []byte) (*File, error) {
	r := &reader{b: b}
	f := &File{}

	if string(r.take(4)) != "8BPS" {
		return nil, fmt.Errorf("not a PSD file")
	}
	ver := r.u16()
	if ver == 2 {
		return nil, fmt.Errorf("%w: large document format (PSB)", ErrUnsupported)
	}
	if ver != 1 {
		return nil, fmt.Errorf("%w: version %d", ErrUnsupported, ver)
	}
	r.take(6)
	f.Header = Header{Channels: r.u16(), Height: r.u32(), Width: r.u32(), Depth: r.u16(), Mode: r.u16()}
	if r.err != nil {
		return nil, r.err
	}
	if f.Header.Depth != 8 {
		return nil, fmt.Errorf("%w: %d-bit depth (only 8-bit supported)", ErrUnsupported, f.Header.Depth)
	}
	if f.Header.Mode != ModeRGB && f.Header.Mode != ModeGrayscale {
		return nil, fmt.Errorf("%w: color mode %s (only RGB and grayscale supported)", ErrUnsupported, modeName(f.Header.Mode))
	}
	if f.Header.Width == 0 || f.Header.Height == 0 {
		return nil, fmt.Errorf("invalid canvas size %dx%d", f.Header.Width, f.Header.Height)
	}
	if f.Header.Width > 30000 || f.Header.Height > 30000 {
		return nil, fmt.Errorf("%w: %dx%d exceeds the PSD size limit", ErrUnsupported, f.Header.Width, f.Header.Height)
	}

	f.ColorData = r.take(int(r.u32()))

	resLen := int(r.u32())
	resEnd := r.pos + resLen
	for r.err == nil && r.pos < resEnd {
		if string(r.take(4)) != "8BIM" {
			return nil, fmt.Errorf("bad image resource signature at %d", r.pos-4)
		}
		res := Resource{ID: r.u16()}
		if r.err != nil || r.pos >= len(r.b) {
			return nil, fmt.Errorf("reading image resources: %w", io.ErrUnexpectedEOF)
		}
		nl := int(r.b[r.pos])
		pl := 1 + nl
		if pl%2 == 1 {
			pl++
		}
		res.Name = r.take(pl)
		sz := int(r.u32())
		res.Data = r.take(sz)
		if sz%2 == 1 {
			r.take(1)
		}
		f.Resources = append(f.Resources, res)
	}
	if r.err != nil {
		return nil, fmt.Errorf("reading image resources: %w", r.err)
	}
	r.pos = resEnd

	lmLen := int(r.u32())
	lmEnd := r.pos + lmLen
	if lmLen > 0 {
		f.hasLayerSection = true
		if err := f.parseLayerAndMask(r, lmEnd); err != nil {
			return nil, err
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("reading layers: %w", r.err)
	}
	if lmEnd > len(b) {
		return nil, fmt.Errorf("reading layers: %w", io.ErrUnexpectedEOF)
	}
	r.pos = lmEnd
	f.Composite = b[r.pos:]
	return f, nil
}

func (f *File) parseLayerAndMask(r *reader, lmEnd int) error {
	liLen := int(r.u32())
	liStart := r.pos
	liEnd := liStart + liLen
	if r.err == nil && liEnd > lmEnd {
		return fmt.Errorf("layer info overruns layer section")
	}
	f.layerPadAlign = 2
	if liLen > 0 {
		cnt := int16(r.u16())
		if cnt < 0 {
			f.MergedAlpha = true
			cnt = -cnt
		}
		var chLens [][]int
		for range int(cnt) {
			if r.err != nil {
				break
			}
			rec, lens := parseRecord(r)
			f.Records = append(f.Records, rec)
			chLens = append(chLens, lens)
		}
		for i, rec := range f.Records {
			for j := range rec.Channels {
				rec.Channels[j].Data = r.take(chLens[i][j])
			}
		}
		if r.err != nil {
			return fmt.Errorf("reading layer records: %w", r.err)
		}
		if r.pos > liEnd {
			return fmt.Errorf("layer records overrun layer info")
		}
		if liLen%4 == 0 && (r.pos-liStart)%4 != 0 {
			f.layerPadAlign = 4
		}
		r.pos = liEnd
	}
	if r.pos < lmEnd {
		gm := int(r.u32())
		f.GlobalMask = r.take(gm)
		if r.err == nil && r.pos > lmEnd {
			return fmt.Errorf("global mask overruns layer section")
		}
	}
	if r.err == nil && r.pos <= lmEnd {
		f.Extra = r.take(lmEnd - r.pos)
	}
	for _, rec := range f.Records {
		if rec.block("Lr16") != nil || rec.block("Lr32") != nil {
			return fmt.Errorf("%w: high bit-depth layers", ErrUnsupported)
		}
	}
	return r.err
}

func parseRecord(r *reader) (*Record, []int) {
	rec := &Record{Top: r.i32(), Left: r.i32(), Bottom: r.i32(), Right: r.i32()}
	nc := int(r.u16())
	lens := make([]int, nc)
	rec.Channels = make([]Channel, nc)
	for i := range nc {
		rec.Channels[i].ID = int16(r.u16())
		lens[i] = int(r.u32())
	}
	rec.BlendSig = r.sig()
	rec.BlendKey = r.sig()
	rec.Opacity = r.u8()
	rec.Clipping = r.u8()
	rec.Flags = r.u8()
	rec.Filler = r.u8()
	exLen := int(r.u32())
	exEnd := r.pos + exLen
	rec.Mask = r.take(int(r.u32()))
	rec.Ranges = r.take(int(r.u32()))
	nl := int(r.b[min(r.pos, len(r.b)-1)])
	pl := (1 + nl + 3) / 4 * 4
	rec.PascalName = r.take(pl)
	for r.err == nil && r.pos+12 <= exEnd {
		b := Block{Sig: r.sig()}
		b.Key = string(r.take(4))
		b.Data = r.take(int(r.u32()))
		rec.Blocks = append(rec.Blocks, b)
	}
	if r.err == nil {
		r.pos = exEnd
	}
	return rec, lens
}

func modeName(m uint16) string {
	switch m {
	case 0:
		return "bitmap"
	case 1:
		return "grayscale"
	case 2:
		return "indexed"
	case 3:
		return "RGB"
	case 4:
		return "CMYK"
	case 7:
		return "multichannel"
	case 8:
		return "duotone"
	case 9:
		return "Lab"
	}
	return fmt.Sprintf("%d", m)
}

// @region psd:write -- SERIALIZING

type writer struct{ bytes.Buffer }

func (w *writer) u8(v byte)    { w.WriteByte(v) }
func (w *writer) u16(v uint16) { w.Write(binary.BigEndian.AppendUint16(nil, v)) }
func (w *writer) u32(v uint32) { w.Write(binary.BigEndian.AppendUint32(nil, v)) }

// writes a 4-byte length placeholder; returns a func that fills it in.
func (w *writer) lenMark() func() int {
	at := w.Len()
	w.u32(0)
	return func() int {
		n := w.Len() - at - 4
		binary.BigEndian.PutUint32(w.Bytes()[at:], uint32(n))
		return n
	}
}

func (f *File) Bytes() []byte {
	w := &writer{}
	w.WriteString("8BPS")
	w.u16(1)
	w.Write(make([]byte, 6))
	h := f.Header
	w.u16(h.Channels)
	w.u32(h.Height)
	w.u32(h.Width)
	w.u16(h.Depth)
	w.u16(h.Mode)

	w.u32(uint32(len(f.ColorData)))
	w.Write(f.ColorData)

	done := w.lenMark()
	for _, res := range f.Resources {
		w.WriteString("8BIM")
		w.u16(res.ID)
		w.Write(res.Name)
		w.u32(uint32(len(res.Data)))
		w.Write(res.Data)
		if len(res.Data)%2 == 1 {
			w.u8(0)
		}
	}
	done()

	if !f.hasLayerSection && len(f.Records) == 0 {
		w.u32(0)
		w.Write(f.Composite)
		return w.Bytes()
	}
	done = w.lenMark()
	f.writeLayerInfo(w)
	w.u32(uint32(len(f.GlobalMask)))
	w.Write(f.GlobalMask)
	w.Write(f.Extra)
	done()

	w.Write(f.Composite)
	return w.Bytes()
}

func (f *File) writeLayerInfo(w *writer) {
	if len(f.Records) == 0 {
		w.u32(0)
		return
	}
	done := w.lenMark()
	start := w.Len()
	cnt := int16(len(f.Records))
	if f.MergedAlpha {
		cnt = -cnt
	}
	w.u16(uint16(cnt))
	for _, rec := range f.Records {
		writeRecord(w, rec)
	}
	for _, rec := range f.Records {
		for _, ch := range rec.Channels {
			w.Write(ch.Data)
		}
	}
	align := max(f.layerPadAlign, 2)
	for (w.Len()-start)%align != 0 {
		w.u8(0)
	}
	done()
}

func writeRecord(w *writer, rec *Record) {
	for _, v := range []int32{rec.Top, rec.Left, rec.Bottom, rec.Right} {
		w.u32(uint32(v))
	}
	w.u16(uint16(len(rec.Channels)))
	for _, ch := range rec.Channels {
		w.u16(uint16(ch.ID))
		w.u32(uint32(len(ch.Data)))
	}
	w.Write(rec.BlendSig[:])
	w.Write(rec.BlendKey[:])
	w.u8(rec.Opacity)
	w.u8(rec.Clipping)
	w.u8(rec.Flags)
	w.u8(rec.Filler)
	done := w.lenMark()
	w.u32(uint32(len(rec.Mask)))
	w.Write(rec.Mask)
	w.u32(uint32(len(rec.Ranges)))
	w.Write(rec.Ranges)
	w.Write(rec.PascalName)
	for _, b := range rec.Blocks {
		w.Write(b.Sig[:])
		w.WriteString(b.Key)
		w.u32(uint32(len(b.Data)))
		w.Write(b.Data)
	}
	done()
}

// @region psd:strings -- NAME ENCODING

func pascalName(s string) []byte {
	b := []byte{0}
	for _, c := range s {
		if len(b) > 255 {
			break
		}
		if c < 0x80 {
			b = append(b, byte(c))
		} else {
			b = append(b, '?')
		}
	}
	b[0] = byte(len(b) - 1)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func encodeLuni(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := binary.BigEndian.AppendUint32(nil, uint32(len(u)))
	for _, c := range u {
		b = binary.BigEndian.AppendUint16(b, c)
	}
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
