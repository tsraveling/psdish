package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/tsraveling/psdish/psd"
)

// @region cli:args -- ARGUMENT PARSING

type opKind int

const (
	opMove opKind = iota
	opRename
	opHide
	opShow
	opToggle
	opDelete
	opGroup
	opIndent
	opOutdent
	opExtract
)

type op struct {
	kind opKind
	ref  string
	arg  string // name, target line, or png path
	into bool
}

type options struct {
	file     string
	cli      bool
	json     bool
	plain    bool
	out      string
	noBackup bool
	force    bool
	help     bool
	version  bool
	ops      []op
}

// arity of each op flag (refs + args after the flag).
var opFlags = map[string]struct {
	kind opKind
	n    int
}{
	"--move":    {opMove, 2},
	"--rename":  {opRename, 2},
	"--hide":    {opHide, 1},
	"--show":    {opShow, 1},
	"--toggle":  {opToggle, 1},
	"--delete":  {opDelete, 1},
	"--group":   {opGroup, 2},
	"--indent":  {opIndent, 1},
	"--outdent": {opOutdent, 1},
	"--extract": {opExtract, 1},
}

func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		if spec, ok := opFlags[a]; ok {
			if i+spec.n >= len(args) {
				return o, fmt.Errorf("%s needs %d argument(s)", a, spec.n)
			}
			p := op{kind: spec.kind, ref: args[i+1]}
			if spec.n == 2 {
				p.arg = args[i+2]
			}
			i += spec.n
			if p.kind == opMove && i+1 < len(args) && args[i+1] == "--into" {
				p.into = true
				i++
			}
			if p.kind == opExtract && i+1 < len(args) && strings.HasSuffix(strings.ToLower(args[i+1]), ".png") {
				p.arg = args[i+1]
				i++
			}
			o.ops = append(o.ops, p)
			continue
		}
		switch a {
		case "-c", "--cli":
			o.cli = true
		case "--json":
			o.json = true
		case "--plain":
			o.plain = true
		case "--no-backup":
			o.noBackup = true
		case "-f", "--force":
			o.force = true
		case "-h", "--help":
			o.help = true
		case "-v", "--version":
			o.version = true
		case "-o", "--out":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a path", a)
			}
			i++
			o.out = args[i]
		case "--into":
			return o, fmt.Errorf("--into must directly follow --move REF LINE")
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				return o, fmt.Errorf("unknown flag %s", a)
			}
			if o.file != "" {
				return o, fmt.Errorf("unexpected argument %q", a)
			}
			o.file = a
		}
	}
	if len(o.ops) > 0 {
		o.cli = true
	}
	return o, nil
}

// @region cli:refs -- LAYER REFERENCES

// resolves a line number, slash path, or unique name to a node.
func resolveRef(d *psd.Doc, ref string) (*psd.Node, error) {
	nodes := d.Nodes()
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(nodes) {
			return nil, fmt.Errorf("line %d out of range (1-%d)", n, len(nodes))
		}
		return nodes[n-1], nil
	}
	var hits []*psd.Node
	for _, n := range nodes {
		if n.Path() == ref {
			hits = append(hits, n)
		}
	}
	if len(hits) == 0 && !strings.Contains(ref, "/") {
		for _, n := range nodes {
			if n.Name() == ref {
				hits = append(hits, n)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no layer or group matches %q", ref)
	case 1:
		return hits[0], nil
	}
	var lines []string
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("%d (%s)", lineOf(nodes, h), h.Path()))
	}
	return nil, fmt.Errorf("%q is ambiguous; use a line number or path: %s", ref, strings.Join(lines, ", "))
}

func lineOf(nodes []*psd.Node, n *psd.Node) int {
	for i, c := range nodes {
		if c == n {
			return i + 1
		}
	}
	return 0
}

// @region cli:listing -- TEXT + JSON OUTPUT

func kindName(n *psd.Node) string {
	if n.IsGroup() {
		return "group"
	}
	if s := n.Special(); s != "" {
		return s
	}
	return "layer"
}

// replaces control characters with '?' for terminal output.
func displayName(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, name)
}

func writeListing(w io.Writer, d *psd.Doc) {
	h := d.File.Header
	fmt.Fprintf(w, "%s  %dx%d\n", filepath.Base(d.Path), h.Width, h.Height)
	nodes := d.Nodes()
	width := len(strconv.Itoa(len(nodes)))
	for i, n := range nodes {
		vis := "on "
		if n.Hidden() {
			vis = "off"
		}
		mark := " "
		if n.Special() != "" {
			mark = "!"
		}
		var tags []string
		if n.HasMask() {
			tags = append(tags, "mask")
		}
		if n.Clipped() {
			tags = append(tags, "clipped")
		}
		suffix := ""
		if len(tags) > 0 {
			suffix = "  [" + strings.Join(tags, ", ") + "]"
		}
		fmt.Fprintf(w, "%*d %s %s %-12s %s%s%s\n", width, i+1, mark, vis, kindName(n), strings.Repeat("  ", n.Depth()), displayName(n.Name()), suffix)
	}
}

type jsonNode struct {
	Line     int    `json:"line"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Depth    int    `json:"depth"`
	Parent   int    `json:"parent"`
	Visible  bool   `json:"visible"`
	Special  bool   `json:"special,omitempty"`
	Mask     bool   `json:"mask,omitempty"`
	Clipped  bool   `json:"clipped,omitempty"`
	Children int    `json:"children,omitempty"`
}

func writeJSON(w io.Writer, d *psd.Doc) error {
	nodes := d.Nodes()
	out := struct {
		File   string     `json:"file"`
		Width  uint32     `json:"width"`
		Height uint32     `json:"height"`
		Layers []jsonNode `json:"layers"`
	}{File: d.Path, Width: d.File.Header.Width, Height: d.File.Header.Height, Layers: []jsonNode{}}
	for i, n := range nodes {
		out.Layers = append(out.Layers, jsonNode{
			Line:     i + 1,
			Name:     n.Name(),
			Path:     n.Path(),
			Type:     kindName(n),
			Depth:    n.Depth(),
			Parent:   lineOf(nodes, n.Parent),
			Visible:  !n.Hidden(),
			Special:  n.Special() != "",
			Mask:     n.HasMask(),
			Clipped:  n.Clipped(),
			Children: len(n.Children),
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// @region cli:run -- APPLY OPS + SAVE

// refs and target lines all resolve against the listing before any op runs,
// so they match the output the caller last saw.
type boundOp struct {
	op
	node   *psd.Node
	target *psd.Node // move: insert before this node (nil = end of top level)
}

func runCLI(o options, stdout, stderr io.Writer) int {
	d, err := psd.Load(o.file)
	if err != nil {
		fmt.Fprintln(stderr, "psdish:", err)
		return 1
	}
	if err := applyOps(d, o, stderr); err != nil {
		fmt.Fprintln(stderr, "psdish:", displayName(err.Error()))
		return 1
	}
	if d.Dirty() || (o.out != "" && hasEdits(o)) {
		path := d.Path
		if o.out != "" {
			path = o.out
		}
		if err := d.SaveAs(path, !o.noBackup); err != nil {
			fmt.Fprintln(stderr, "psdish: save failed:", err)
			return 1
		}
	}
	if o.json {
		if err := writeJSON(stdout, d); err != nil {
			fmt.Fprintln(stderr, "psdish:", err)
			return 1
		}
	} else if !extractOnly(o) {
		writeListing(stdout, d)
	}
	return 0
}

func hasEdits(o options) bool {
	for _, p := range o.ops {
		if p.kind != opExtract {
			return true
		}
	}
	return false
}

func extractOnly(o options) bool { return len(o.ops) > 0 && !hasEdits(o) }

func applyOps(d *psd.Doc, o options, stderr io.Writer) error {
	nodes := d.Nodes()
	var bound []boundOp
	for _, p := range o.ops {
		n, err := resolveRef(d, p.ref)
		if err != nil {
			return err
		}
		b := boundOp{op: p, node: n}
		if p.kind == opMove {
			line, err := strconv.Atoi(p.arg)
			if err != nil {
				return fmt.Errorf("--move target must be a line number, got %q", p.arg)
			}
			switch {
			case line == len(nodes)+1 && !p.into:
				b.target = nil
			case line < 1 || line > len(nodes):
				return fmt.Errorf("--move target line %d out of range (1-%d)", line, len(nodes)+1)
			default:
				b.target = nodes[line-1]
				if p.into && !b.target.IsGroup() {
					return fmt.Errorf("--into target line %d is not a group", line)
				}
			}
		}
		bound = append(bound, b)
	}

	for _, b := range bound {
		n := d.Find(b.node.ID)
		if n == nil {
			return fmt.Errorf("%q was deleted by an earlier operation", b.ref)
		}
		switch b.kind {
		case opMove:
			parent, idx := d.Root, len(d.Root.Children)
			if b.target != nil {
				t := d.Find(b.target.ID)
				if t == nil {
					return fmt.Errorf("move target for %q was deleted by an earlier operation", b.ref)
				}
				if t == n || t.HasAncestor(n) {
					return fmt.Errorf("cannot move %q into itself", b.ref)
				}
				parent, idx = t.Parent, t.Index()
				if b.into {
					parent, idx = t, len(t.Children)
				}
			}
			if !d.MoveTo(n, parent, idx) {
				fmt.Fprintf(stderr, "psdish: --move %s: cannot move there or already in place; skipped\n", b.ref)
			}
		case opRename:
			d.Rename(n, b.arg)
		case opHide:
			d.SetVisible(n, false)
		case opShow:
			d.SetVisible(n, true)
		case opToggle:
			d.ToggleVisible(n)
		case opDelete:
			d.Delete(n)
		case opGroup:
			if d.Wrap(n, b.arg) == nil {
				fmt.Fprintf(stderr, "psdish: --group %s: cannot be grouped; skipped\n", b.ref)
			}
		case opIndent:
			if !d.Indent(n) {
				fmt.Fprintf(stderr, "psdish: --indent %s: no group directly above; skipped\n", b.ref)
			}
		case opOutdent:
			if !d.Outdent(n) {
				fmt.Fprintf(stderr, "psdish: --outdent %s: already at top level; skipped\n", b.ref)
			}
		case opExtract:
			path := b.arg
			if path == "" {
				path = defaultExportName(n.Name())
			}
			path = siblingPath(d.Path, path)
			if err := exportPNG(d, n, path, o.force); err != nil {
				return err
			}
			fmt.Fprintln(stderr, "wrote", path)
		}
	}
	return nil
}

// @region cli:export -- PNG EXPORT

var errExists = errors.New("file exists")

func defaultExportName(name string) string {
	return sanitizeFilename(name) + ".png"
}

func sanitizeFilename(name string) string {
	s := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		s = "layer"
	}
	return s
}

// relative names land next to the psd.
func siblingPath(psdPath, name string) string {
	if filepath.IsAbs(name) || strings.ContainsRune(name, filepath.Separator) {
		return name
	}
	return filepath.Join(filepath.Dir(psdPath), name)
}

func exportPNG(d *psd.Doc, n *psd.Node, path string, overwrite bool) error {
	if s := n.Special(); s != "" && n.Rec.Bounds().Empty() {
		return fmt.Errorf("%s layers have no pixels to export", s)
	}
	if _, err := os.Stat(path); err == nil && !overwrite {
		return fmt.Errorf("%s: %w (use --force to overwrite)", path, errExists)
	}
	img, err := d.Image(n)
	if err != nil {
		return err
	}
	if img == nil {
		name := filepath.Base(d.Path)
		if !n.IsRoot() {
			name = n.Name()
		}
		return fmt.Errorf("%q has no visible pixels", name)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
