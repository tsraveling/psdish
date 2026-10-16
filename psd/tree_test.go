package psd

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

func layerRec(name string) *Record {
	r := &Record{Channels: emptyGroupChannels(), BlendSig: sig8BIM, BlendKey: [4]byte{'n', 'o', 'r', 'm'}, Opacity: 255, groupEnabled: 1}
	r.SetName(name)
	return r
}

// builds a doc from an indented spec, top-most first. "+" prefix marks groups.
//
//	+A
//	  x
//	y
func docFrom(t *testing.T, spec string) *Doc {
	t.Helper()
	type frame struct {
		indent int
		name   string
	}
	var recs []*Record // display order, converted to file order at the end
	var stack []frame
	var id uint32 = 100
	closeTo := func(indent int) {
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			id++
			recs = append(recs, newDividerRecord(id))
			stack = stack[:len(stack)-1]
		}
	}
	for _, line := range strings.Split(strings.Trim(spec, "\n"), "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		name := strings.TrimSpace(line)
		closeTo(indent)
		id++
		if strings.HasPrefix(name, "+") {
			recs = append(recs, newGroupRecord(name[1:], id))
			stack = append(stack, frame{indent, name[1:]})
		} else {
			recs = append(recs, layerRec(name))
		}
	}
	closeTo(0)
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	f := &File{Header: Header{Channels: 3, Width: 4, Height: 4, Depth: 8, Mode: ModeRGB}, Records: recs, hasLayerSection: true, layerPadAlign: 4}
	f.Composite = []byte{0, 0}
	return NewDoc(f)
}

func dump(d *Doc) string {
	var b strings.Builder
	for _, n := range d.Nodes() {
		b.WriteString(strings.Repeat("  ", n.Depth()))
		if n.IsGroup() {
			b.WriteString("+")
		}
		b.WriteString(n.Name())
		if n.Hidden() {
			b.WriteString(" (h)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func byName(d *Doc, name string) *Node {
	for _, n := range d.Nodes() {
		if n.Name() == name {
			return n
		}
	}
	return nil
}

const base = `
+A
  +B
    l1
    l2
  l3
+C
  l4
l5
`

func trimSpec(s string) string { return strings.TrimLeft(s, "\n") }

func check(t *testing.T, d *Doc, want string) {
	t.Helper()
	if got := dump(d); got != trimSpec(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, trimSpec(want))
	}
	// structure must survive a write/read cycle
	f, err := Parse(d.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := dump(NewDoc(f)); got != trimSpec(want) {
		t.Fatalf("after roundtrip got:\n%s\nwant:\n%s", got, trimSpec(want))
	}
}

func TestBuild(t *testing.T) {
	check(t, docFrom(t, base), base)
}

func TestShift(t *testing.T) {
	d := docFrom(t, base)
	if d.Shift(byName(d, "l1"), -1) {
		t.Fatal("shift past top should no-op")
	}
	if d.Shift(byName(d, "l5"), 1) {
		t.Fatal("shift past bottom should no-op")
	}
	d.Shift(byName(d, "l1"), 1)
	d.Shift(byName(d, "C"), -1)
	check(t, d, `
+C
  l4
+A
  +B
    l2
    l1
  l3
l5
`)
}

func TestIndentOutdent(t *testing.T) {
	d := docFrom(t, base)
	if d.Indent(byName(d, "A")) {
		t.Fatal("indent with nothing above should no-op")
	}
	if d.Indent(byName(d, "l2")) {
		t.Fatal("indent under a layer should no-op")
	}
	if d.Outdent(byName(d, "l5")) {
		t.Fatal("outdent at root should no-op")
	}
	d.Indent(byName(d, "l3")) // into B, at bottom
	d.Indent(byName(d, "l5")) // into C
	d.Outdent(byName(d, "l1"))
	check(t, d, `
+A
  +B
    l2
    l3
  l1
+C
  l4
  l5
`)
}

func TestWrapDeleteRenameToggle(t *testing.T) {
	d := docFrom(t, base)
	g := d.Wrap(byName(d, "l3"), "W")
	if g == nil || g.Name() != "W" {
		t.Fatal("wrap failed")
	}
	d.Delete(byName(d, "C"))
	d.Rename(byName(d, "l1"), "renamed ünïcode")
	d.ToggleVisible(byName(d, "B"))
	check(t, d, `
+A
  +B (h)
    renamed ünïcode
    l2
  +W
    l3
l5
`)
}

func TestUndoRedoDirty(t *testing.T) {
	d := docFrom(t, base)
	if d.Dirty() {
		t.Fatal("fresh doc dirty")
	}
	d.Delete(byName(d, "A"))
	d.ToggleVisible(byName(d, "l5"))
	if !d.Dirty() {
		t.Fatal("should be dirty")
	}
	d.Undo()
	d.Undo()
	if d.Dirty() {
		t.Fatal("undo to saved state should be clean")
	}
	check(t, d, base)
	d.Redo()
	if byName(d, "A") != nil {
		t.Fatal("redo failed")
	}
	if d.Undo(); d.Redo() && d.Redo() {
		if !byName(d, "l5").Hidden() {
			t.Fatal("redo toggle failed")
		}
	}
}

func TestMoveTo(t *testing.T) {
	d := docFrom(t, base)
	if d.MoveTo(byName(d, "A"), byName(d, "B"), 0) {
		t.Fatal("moving group into own descendant should fail")
	}
	d.MoveTo(byName(d, "l5"), byName(d, "B"), 1)
	d.MoveTo(byName(d, "A"), d.Root, 2)
	check(t, d, `
+C
  l4
+A
  +B
    l1
    l5
    l2
  l3
`)
}

func TestFixtureEditsRoundtrip(t *testing.T) {
	b := loadFixture(t)
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDoc(f)
	before := dump(d)
	nodes := d.Nodes()
	d.Shift(nodes[0], 1)
	d.Wrap(nodes[3], "Wrapped")
	d.Delete(nodes[5])
	d.Rename(nodes[1], "Renamed")
	want := dump(d)
	if want == before {
		t.Fatal("edits did nothing")
	}
	f2, err := Parse(d.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := dump(NewDoc(f2)); got != want {
		t.Fatalf("roundtrip mismatch:\n%s\nvs\n%s", got, want)
	}
}

func TestFixtureResourcesAndSave(t *testing.T) {
	b := loadFixture(t)
	dir := t.TempDir()
	path := dir + "/f.psd"
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Delete(d.Nodes()[0])
	d.Wrap(d.Nodes()[0], "G")
	if err := d.Save(true); err != nil {
		t.Fatal(err)
	}
	if d.Dirty() {
		t.Fatal("dirty after save")
	}
	if bak, _ := os.ReadFile(path + ".bak"); !bytes.Equal(bak, b) {
		t.Fatal("backup missing or wrong")
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	n := len(f.Records)
	if r := f.resource(1026); len(r.Data) != 2*n {
		t.Fatalf("1026 len %d, want %d", len(r.Data), 2*n)
	}
	if r := f.resource(1072); len(r.Data) != n {
		t.Fatalf("1072 len %d, want %d", len(r.Data), n)
	}
	if r := f.resource(1024); int(binary.BigEndian.Uint16(r.Data)) >= n {
		t.Fatal("1024 out of range")
	}
}
