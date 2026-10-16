package psd

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// @region psd:tree -- HIERARCHY MODEL

type Node struct {
	ID       int     // session-stable; survives undo snapshots
	Rec      *Record // layer record, or group opener
	End      *Record // group end divider; nil for layers
	Parent   *Node
	Children []*Node // display order: top-most first
}

func (n *Node) IsGroup() bool { return n.End != nil }
func (n *Node) IsRoot() bool  { return n.Parent == nil }

// unmatched end divider; must stay top level or it closes its parent on reload.
func (n *Node) orphanDivider() bool {
	return !n.IsGroup() && n.Rec != nil && n.Rec.SectionType() == SectionEnd
}
func (n *Node) Name() string { return n.Rec.Name() }
func (n *Node) Hidden() bool { return n.Rec.Hidden() }

// true when the file stored this group as open in Photoshop's layer panel.
func (n *Node) FileOpen() bool { return n.Rec.SectionType() != SectionClosed }

// hidden itself or via any ancestor.
func (n *Node) EffectivelyHidden() bool {
	for c := n; c != nil && !c.IsRoot(); c = c.Parent {
		if c.Hidden() {
			return true
		}
	}
	return false
}

func (n *Node) Index() int {
	if n.Parent == nil {
		return 0
	}
	return slices.Index(n.Parent.Children, n)
}

func (n *Node) Depth() int {
	d := -1
	for c := n; c.Parent != nil; c = c.Parent {
		d++
	}
	return d
}

func (n *Node) HasAncestor(a *Node) bool {
	for c := n.Parent; c != nil; c = c.Parent {
		if c == a {
			return true
		}
	}
	return false
}

// all descendants in display order.
func (n *Node) Walk(fn func(*Node)) {
	for _, c := range n.Children {
		fn(c)
		c.Walk(fn)
	}
}

func (n *Node) DescendantCount() int {
	k := 0
	n.Walk(func(*Node) { k++ })
	return k
}

// "A/B/C" from top level.
func (n *Node) Path() string {
	if n.Parent == nil || n.Parent.IsRoot() {
		return n.Name()
	}
	return n.Parent.Path() + "/" + n.Name()
}

func (n *Node) clone(parent *Node) *Node {
	c := &Node{ID: n.ID, Parent: parent}
	if n.Rec != nil {
		c.Rec = n.Rec.clone()
	}
	if n.End != nil {
		c.End = n.End.clone()
	}
	c.Children = make([]*Node, len(n.Children))
	for i, ch := range n.Children {
		c.Children[i] = ch.clone(c)
	}
	return c
}

// @region psd:doc -- DOCUMENT + UNDO

type Doc struct {
	Path string
	File *File
	Root *Node

	nextID   int
	undo     []*Node
	redo     []*Node
	saved    *Node // snapshot at last save
	loaded   *Node // snapshot at load
	backedUp map[string]bool

	hasGroupInfo    bool   // resource 1026 matched layer count at load
	hasGroupEnabled bool   // resource 1072 matched layer count at load
	loadedVersion   []byte // resource 1057 data at load; nil when absent
}

func Load(path string) (*Doc, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	d := NewDoc(f)
	d.Path = path
	return d, nil
}

func NewDoc(f *File) *Doc {
	d := &Doc{File: f}
	n := len(f.Records)
	if res := f.resource(1024); res != nil && len(res.Data) == 2 {
		if t := int(binary.BigEndian.Uint16(res.Data)); t < n {
			f.Records[t].target = true
		}
	}
	if res := f.resource(1026); res != nil && len(res.Data) == 2*n {
		d.hasGroupInfo = true
		for i, r := range f.Records {
			r.groupInfo = binary.BigEndian.Uint16(res.Data[2*i:])
		}
	}
	for _, r := range f.Records {
		r.groupEnabled = 1
	}
	if res := f.resource(1072); res != nil && len(res.Data) == n {
		d.hasGroupEnabled = true
		for i, r := range f.Records {
			r.groupEnabled = res.Data[i]
		}
	}
	if res := f.resource(1057); res != nil {
		d.loadedVersion = res.Data
	}
	d.Root = d.build(f.Records)
	d.saved = d.Root.clone(nil)
	d.loaded = d.saved
	return d
}

func (d *Doc) newID() int {
	d.nextID++
	return d.nextID
}

func (d *Doc) build(recs []*Record) *Node {
	root := &Node{ID: d.newID()}
	cur := root
	for i := len(recs) - 1; i >= 0; i-- {
		rec := recs[i]
		switch rec.SectionType() {
		case SectionOpen, SectionClosed:
			g := &Node{ID: d.newID(), Rec: rec, Parent: cur}
			cur.Children = append(cur.Children, g)
			cur = g
		case SectionEnd:
			if cur == root {
				// orphan divider: keep it so the file still roundtrips
				cur.Children = append(cur.Children, &Node{ID: d.newID(), Rec: rec, Parent: cur})
				continue
			}
			cur.End = rec
			cur = cur.Parent
		default:
			cur.Children = append(cur.Children, &Node{ID: d.newID(), Rec: rec, Parent: cur})
		}
	}
	// unclosed groups get synthesized dividers
	for ; cur != root; cur = cur.Parent {
		cur.End = newDividerRecord(d.nextLayerID(root))
	}
	return root
}

// flat display-order list of every node.
func (d *Doc) Nodes() []*Node {
	var out []*Node
	d.Root.Walk(func(n *Node) { out = append(out, n) })
	return out
}

func (d *Doc) Find(id int) *Node {
	var found *Node
	d.Root.Walk(func(n *Node) {
		if n.ID == id {
			found = n
		}
	})
	return found
}

func (d *Doc) checkpoint() {
	d.undo = append(d.undo, d.Root.clone(nil))
	d.redo = nil
}

func (d *Doc) CanUndo() bool { return len(d.undo) > 0 }
func (d *Doc) CanRedo() bool { return len(d.redo) > 0 }

func (d *Doc) Undo() bool {
	if len(d.undo) == 0 {
		return false
	}
	d.redo = append(d.redo, d.Root)
	d.Root = d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]
	return true
}

func (d *Doc) Redo() bool {
	if len(d.redo) == 0 {
		return false
	}
	d.undo = append(d.undo, d.Root)
	d.Root = d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]
	return true
}

func (d *Doc) Dirty() bool {
	return d.saved == nil || !sameTree(d.Root, d.saved, true)
}

// names: also compare layer names (they do not affect pixels).
func sameTree(a, b *Node, names bool) bool {
	if a.ID != b.ID || len(a.Children) != len(b.Children) {
		return false
	}
	if a.Rec != nil && (a.Rec.Flags != b.Rec.Flags || names && a.Rec.Name() != b.Rec.Name()) {
		return false
	}
	for i := range a.Children {
		if !sameTree(a.Children[i], b.Children[i], names) {
			return false
		}
	}
	return true
}

// @region psd:ops -- EDIT OPERATIONS
// each op returns false (and records no undo step) when it would do nothing.

func (d *Doc) ToggleVisible(n *Node) bool {
	return d.SetVisible(n, n.Hidden())
}

func (d *Doc) SetVisible(n *Node, visible bool) bool {
	if n.IsRoot() || n.Hidden() == !visible {
		return false
	}
	d.checkpoint()
	n = d.Find(n.ID)
	n.Rec.SetHidden(!visible)
	return true
}

func (d *Doc) Rename(n *Node, name string) bool {
	if n.IsRoot() || name == "" || name == n.Name() {
		return false
	}
	d.checkpoint()
	n = d.Find(n.ID)
	n.Rec.SetName(name)
	return true
}

// moves n one slot up (-1) or down (+1) among its siblings.
func (d *Doc) Shift(n *Node, delta int) bool {
	if n.IsRoot() {
		return false
	}
	i := n.Index()
	j := i + delta
	if j < 0 || j >= len(n.Parent.Children) {
		return false
	}
	d.checkpoint()
	n = d.Find(n.ID)
	s := n.Parent.Children
	s[i], s[j] = s[j], s[i]
	return true
}

// moves n to the bottom of the group directly above it.
func (d *Doc) Indent(n *Node) bool {
	if n.IsRoot() {
		return false
	}
	i := n.Index()
	if i == 0 || !n.Parent.Children[i-1].IsGroup() {
		return false
	}
	target := n.Parent.Children[i-1]
	return d.MoveTo(n, target, len(target.Children))
}

// moves n out of its group to directly below that group.
func (d *Doc) Outdent(n *Node) bool {
	if n.IsRoot() || n.Parent.IsRoot() {
		return false
	}
	p := n.Parent
	return d.MoveTo(n, p.Parent, p.Index()+1)
}

// moves n into parent at index (index counted before n is removed).
func (d *Doc) MoveTo(n, parent *Node, index int) bool {
	if n.orphanDivider() && !parent.IsRoot() {
		return false
	}
	if n.IsRoot() || parent == n || parent.HasAncestor(n) || (!parent.IsGroup() && !parent.IsRoot()) {
		return false
	}
	if parent == n.Parent {
		i := n.Index()
		if index == i || index == i+1 {
			return false
		}
	}
	d.checkpoint()
	n, parent = d.Find(n.ID), d.findOrRoot(parent.ID)
	if parent == n.Parent && index > n.Index() {
		index--
	}
	detach(n)
	index = max(0, min(index, len(parent.Children)))
	parent.Children = slices.Insert(parent.Children, index, n)
	n.Parent = parent
	return true
}

// wraps n in a new group named name; returns the group.
func (d *Doc) Wrap(n *Node, name string) *Node {
	if n.IsRoot() || name == "" || n.orphanDivider() {
		return nil
	}
	d.checkpoint()
	n = d.Find(n.ID)
	p, i := n.Parent, n.Index()
	g := &Node{
		ID:     d.newID(),
		Rec:    newGroupRecord(name, d.nextLayerID(d.Root)),
		Parent: p,
	}
	g.End = newDividerRecord(d.nextLayerID(d.Root) + 1)
	p.Children[i] = g
	g.Children = []*Node{n}
	n.Parent = g
	return g
}

func (d *Doc) Delete(n *Node) bool {
	if n.IsRoot() {
		return false
	}
	d.checkpoint()
	detach(d.Find(n.ID))
	return true
}

func (d *Doc) findOrRoot(id int) *Node {
	if d.Root.ID == id {
		return d.Root
	}
	return d.Find(id)
}

func detach(n *Node) {
	p := n.Parent
	p.Children = slices.DeleteFunc(p.Children, func(c *Node) bool { return c == n })
	n.Parent = nil
}

func (d *Doc) nextLayerID(root *Node) uint32 {
	var hi uint32
	see := func(r *Record) {
		if r == nil {
			return
		}
		if id, ok := r.LayerID(); ok && id > hi {
			hi = id
		}
	}
	root.Walk(func(n *Node) {
		see(n.Rec)
		see(n.End)
	})
	return hi + 1
}

func emptyGroupChannels() []Channel {
	ch := make([]Channel, 4)
	for i := range ch {
		ch[i] = Channel{ID: int16(i - 1), Data: []byte{0, 0}}
	}
	return ch
}

func newGroupRecord(name string, id uint32) *Record {
	lsct := binary.BigEndian.AppendUint32(nil, SectionOpen)
	lsct = append(lsct, "8BIMpass"...)
	r := &Record{
		Channels:     emptyGroupChannels(),
		BlendSig:     sig8BIM,
		BlendKey:     [4]byte{'n', 'o', 'r', 'm'},
		Opacity:      255,
		Flags:        0x18,
		groupEnabled: 1,
		Blocks: []Block{
			{Sig: sig8BIM, Key: "lyid", Data: binary.BigEndian.AppendUint32(nil, id)},
			{Sig: sig8BIM, Key: "lsct", Data: lsct},
		},
	}
	r.SetName(name)
	return r
}

func newDividerRecord(id uint32) *Record {
	r := &Record{
		Channels:     emptyGroupChannels(),
		BlendSig:     sig8BIM,
		BlendKey:     [4]byte{'n', 'o', 'r', 'm'},
		Opacity:      255,
		Flags:        0x18,
		groupEnabled: 1,
		Blocks: []Block{
			{Sig: sig8BIM, Key: "lyid", Data: binary.BigEndian.AppendUint32(nil, id)},
			{Sig: sig8BIM, Key: "lsct", Data: binary.BigEndian.AppendUint32(nil, SectionEnd)},
		},
	}
	r.SetName("</Layer group>")
	return r
}

// @region psd:save -- FLATTEN + SAFE SAVE

// flattens the tree back into File.Records and syncs layer-indexed resources.
func (d *Doc) commit() {
	var recs []*Record
	var emit func(n *Node)
	emit = func(n *Node) {
		if n.IsGroup() {
			recs = append(recs, n.End)
		}
		for i := len(n.Children) - 1; i >= 0; i-- {
			emit(n.Children[i])
		}
		if n.Rec != nil {
			recs = append(recs, n.Rec)
		}
	}
	emit(d.Root)
	d.File.Records = recs
	if sameTree(d.Root, d.loaded, false) {
		d.restoreVersionInfo()
	} else {
		d.markCompositeStale()
	}
	d.syncResources(recs)
}

// clears hasRealMergedData in resource 1057, adding or replacing it if missing or malformed.
func (d *Doc) markCompositeStale() {
	f := d.File
	res := f.resource(1057)
	switch {
	case res == nil:
		f.Resources = append(f.Resources, Resource{ID: 1057, Name: []byte{0, 0}, Data: staleVersionInfo()})
	case len(res.Data) < 5:
		res.Data = staleVersionInfo()
	case res.Data[4] != 0:
		res.Data = append([]byte(nil), res.Data...)
		res.Data[4] = 0
	}
}

func staleVersionInfo() []byte {
	data := binary.BigEndian.AppendUint32(nil, 1)
	data = append(data, 0)
	data = append(data, encodeLuni("")...) // writer name
	data = append(data, encodeLuni("")...) // reader name
	return binary.BigEndian.AppendUint32(data, 1)
}

// puts resource 1057 back as loaded, undoing an earlier save's markCompositeStale.
func (d *Doc) restoreVersionInfo() {
	f := d.File
	res := f.resource(1057)
	switch {
	case res == nil:
	case d.loadedVersion == nil:
		f.Resources = slices.DeleteFunc(f.Resources, func(r Resource) bool { return r.ID == 1057 })
	default:
		res.Data = d.loadedVersion
	}
}

func (d *Doc) syncResources(recs []*Record) {
	f := d.File
	if res := f.resource(1024); res != nil && len(res.Data) == 2 {
		t := 0
		for i, r := range recs {
			if r.target {
				t = i
			}
		}
		res.Data = binary.BigEndian.AppendUint16(nil, uint16(t))
	}
	if res := f.resource(1026); res != nil && d.hasGroupInfo {
		res.Data = res.Data[:0:0]
		for _, r := range recs {
			res.Data = binary.BigEndian.AppendUint16(res.Data, r.groupInfo)
		}
	}
	if res := f.resource(1072); res != nil && d.hasGroupEnabled {
		res.Data = res.Data[:0:0]
		for _, r := range recs {
			res.Data = append(res.Data, r.groupEnabled)
		}
	}
	if res := f.resource(1069); res != nil && len(res.Data) >= 2 {
		live := map[uint32]bool{}
		for _, r := range recs {
			if id, ok := r.LayerID(); ok {
				live[id] = true
			}
		}
		n := int(binary.BigEndian.Uint16(res.Data))
		out := []byte{0, 0}
		k := 0
		for i := 0; i < n && 2+4*i+4 <= len(res.Data); i++ {
			id := binary.BigEndian.Uint32(res.Data[2+4*i:])
			if live[id] {
				out = binary.BigEndian.AppendUint32(out, id)
				k++
			}
		}
		binary.BigEndian.PutUint16(out, uint16(k))
		res.Data = out
	}
}

func (f *File) resource(id uint16) *Resource {
	for i := range f.Resources {
		if f.Resources[i].ID == id {
			return &f.Resources[i]
		}
	}
	return nil
}

// serializes the current tree; the Doc's File reflects the tree afterwards.
func (d *Doc) Bytes() []byte {
	d.commit()
	return d.File.Bytes()
}

// writes to path via temp file + verification re-parse + rename. When backup is
// set and path already exists, the existing file is first copied to path.bak
// (once per doc).
func (d *Doc) SaveAs(path string, backup bool) error {
	b := d.Bytes()
	if _, err := Parse(b); err != nil {
		return fmt.Errorf("verification failed, not saved: %w", err)
	}
	if backup && !d.backedUp[path] {
		if orig, err := os.ReadFile(path); err == nil {
			if err := os.WriteFile(path+".bak", orig, 0o644); err != nil {
				return fmt.Errorf("writing backup: %w", err)
			}
			if d.backedUp == nil {
				d.backedUp = map[string]bool{}
			}
			d.backedUp[path] = true
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".psdish-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		os.Chmod(tmp.Name(), info.Mode().Perm())
	} else {
		os.Chmod(tmp.Name(), 0o644)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d.Path = path
	d.saved = d.Root.clone(nil)
	return nil
}

func (d *Doc) Save(backup bool) error { return d.SaveAs(d.Path, backup) }

// read-only copy safe to use from another goroutine while d keeps changing.
func (d *Doc) Snapshot() *Doc {
	// own copy of the resource list: commit rewrites resource data on save
	f := *d.File
	f.Resources = slices.Clone(f.Resources)
	return &Doc{Path: d.Path, File: &f, Root: d.Root.clone(nil), loaded: d.loaded, loadedVersion: d.loadedVersion}
}

// true when layer order or visibility differs from the file as loaded.
func (d *Doc) PixelsChanged() bool {
	return d.loaded != nil && !sameTree(d.Root, d.loaded, false)
}
