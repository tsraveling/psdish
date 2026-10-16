package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tsraveling/psdish/psd"
)

func testModel(t *testing.T, w, h int) *model {
	t.Helper()
	d, err := psd.Load(fixtureCopy(t))
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(d, options{plain: true}, newPreviewer(nil, graphicsBlocks))
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func key(m *model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "up", "down", "left", "right", "tab", "enter", "esc", "ctrl+s", "ctrl+a", "ctrl+c", "backspace", " ":
			msg = tea.KeyMsg(map[string]tea.Key{
				"up": {Type: tea.KeyUp}, "down": {Type: tea.KeyDown}, "left": {Type: tea.KeyLeft},
				"right": {Type: tea.KeyRight}, "tab": {Type: tea.KeyTab}, "enter": {Type: tea.KeyEnter},
				"esc": {Type: tea.KeyEsc}, "ctrl+s": {Type: tea.KeyCtrlS}, "ctrl+a": {Type: tea.KeyCtrlA},
				"ctrl+c": {Type: tea.KeyCtrlC}, "backspace": {Type: tea.KeyBackspace}, " ": {Type: tea.KeySpace, Runes: []rune{' '}},
			}[k])
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.Update(msg)
	}
}

func screen(m *model) string { return ansi.Strip(m.View()) }

func cur(m *model) string {
	if n := m.node(); n != nil {
		return n.Name()
	}
	return "<file>"
}

func TestTUINavigationAndCollapse(t *testing.T) {
	m := testModel(t, 160, 50)
	s := screen(m)
	if !strings.Contains(s, "fixture.psd") || !strings.Contains(s, "eyellids") {
		t.Fatal(s)
	}
	// "Brlink UP" is stored closed: its children are hidden and it shows a count
	if strings.Contains(s, "Blink up_L") || !strings.Contains(s, "Brlink UP (2)") {
		t.Fatal("closed group not collapsed:\n" + s)
	}
	if cur(m) != "<file>" {
		t.Fatal("cursor should start on filename")
	}
	key(m, "l")
	if cur(m) != "head" {
		t.Fatal(cur(m))
	}
	key(m, "l")
	if cur(m) != "eyellids" {
		t.Fatal(cur(m))
	}
	key(m, "h", "h")
	if cur(m) != "<file>" {
		t.Fatal("h from top level should reach filename row, got", cur(m))
	}
	key(m, "j", "j", "tab")
	if !strings.Contains(screen(m), "eyellids (4)") {
		t.Fatal("tab should collapse eyellids")
	}
	key(m, "j")
	if cur(m) != "Eyes" {
		t.Fatal("j should skip collapsed children, got", cur(m))
	}
	key(m, "k", "l")
	if cur(m) != "eyelid_L" || strings.Contains(screen(m), "eyellids (4)") {
		t.Fatal("l on collapsed group should expand + enter, got", cur(m))
	}
	if m.doc.Dirty() {
		t.Fatal("folding must not dirty the doc")
	}
}

func TestTUIEditsUndoSave(t *testing.T) {
	m := testModel(t, 160, 50)
	key(m, "j", "j", "j") // eyelid_L
	key(m, " ")
	if !m.node().Hidden() || !m.doc.Dirty() {
		t.Fatal("space should hide")
	}
	key(m, "J")
	if m.node().Index() != 1 {
		t.Fatal("J should move down")
	}
	key(m, "H")
	if m.node().Parent.Name() != "head" {
		t.Fatal("H should outdent")
	}
	key(m, "r")
	for range 20 {
		key(m, "backspace")
	}
	key(m, "L", "i", "d", "enter")
	if cur(m) != "Lid" {
		t.Fatal("rename failed:", cur(m))
	}
	key(m, "g", "W", "r", "a", "p", "enter")
	if m.node().Parent.Name() != "Wrap" {
		t.Fatal("wrap failed")
	}
	key(m, "u")
	if m.node().Parent.Name() == "Wrap" {
		t.Fatal("undo wrap failed")
	}
	key(m, "U")
	if m.node().Parent.Name() != "Wrap" {
		t.Fatal("redo wrap failed")
	}
	key(m, "d", "n")
	if m.node() == nil || cur(m) != "Lid" {
		t.Fatal("n should cancel delete")
	}
	key(m, "d", "y")
	if byNameT(m.doc, "Lid") != nil {
		t.Fatal("delete failed")
	}
	key(m, "ctrl+s")
	if m.doc.Dirty() || !strings.Contains(m.status, "saved") {
		t.Fatal("save failed:", m.status)
	}
	again, err := psd.Load(m.doc.Path)
	if err != nil || byNameT(again, "Wrap") == nil {
		t.Fatal("saved file missing edit", err)
	}
}

func TestTUIQuitGuardAndSaveAs(t *testing.T) {
	m := testModel(t, 100, 60)
	key(m, "j", " ")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.confirm == nil || m.confirm.kind != confirmQuit {
		t.Fatal("dirty quit should confirm")
	}
	_ = cmd
	key(m, "n")
	// save as with unchanged name still asks to overwrite
	key(m, "ctrl+a", "enter")
	if m.confirm == nil || m.confirm.kind != confirmOverwriteSave {
		t.Fatal("save-as onto existing file should confirm overwrite")
	}
	key(m, "esc", "ctrl+a")
	for range len("fixture.psd") {
		key(m, "backspace")
	}
	key(m, "c", "o", "p", "y", ".", "p", "s", "d", "enter")
	want := filepath.Join(filepath.Dir(m.doc.Path), "copy.psd")
	if m.doc.Path != want || m.doc.Dirty() {
		t.Fatal("save as failed", m.doc.Path, m.status)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestTUIExportAndPreview(t *testing.T) {
	m := testModel(t, 160, 50)
	key(m, "j", "j", "e", "enter")
	path := filepath.Join(filepath.Dir(m.doc.Path), "eyellids.png")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("export missing:", m.status)
	}
	key(m, "e", "enter")
	if m.confirm == nil || m.confirm.kind != confirmOverwriteExport {
		t.Fatal("export overwrite should confirm")
	}
	key(m, "y")

	// drive the preview pipeline synchronously
	k := m.previewKey()
	c := m.preview.render(k, m.doc.Snapshot(), 0)
	m.handlePreviewDone(c)
	if len(c.lines) == 0 || c.message != "" {
		t.Fatal("expected block preview", c.message)
	}
	if !strings.Contains(screen(m), c.caption) {
		t.Fatal("caption not shown")
	}
	for _, l := range c.lines {
		if ansi.StringWidth(l) > k.w {
			t.Fatal("preview line wider than pane")
		}
	}
}

func TestTUILayoutFits(t *testing.T) {
	for _, sz := range [][2]int{{160, 50}, {80, 60}, {40, 12}, {200, 30}} {
		m := testModel(t, sz[0], sz[1])
		lines := strings.Split(m.View(), "\n")
		if len(lines) != sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > sz[0] {
				t.Errorf("%v: line too wide: %d", sz, ansi.StringWidth(l))
				break
			}
		}
	}
}

func byNameT(d *psd.Doc, name string) *psd.Node {
	for _, n := range d.Nodes() {
		if n.Name() == name {
			return n
		}
	}
	return nil
}
