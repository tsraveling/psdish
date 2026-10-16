package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	overlay "github.com/rmhubbert/bubbletea-overlay"
	"github.com/tsraveling/psdish/psd"
)

// @region tui:model -- STATE

type model struct {
	doc    *psd.Doc
	opts   options
	glyphs glyphs

	w, h      int
	cursor    int // node ID; 0 = filename row
	scroll    int
	collapsed map[int]bool
	rev       int // bumps on every document change

	prompt  *prompt
	confirm *confirm
	status  string
	isErr   bool

	preview *previewer
}

func runTUI(o options) int {
	d, err := psd.Load(o.file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "psdish:", err)
		return 1
	}
	out := &lockedOutput{f: os.Stdout}
	m := newModel(d, o, newPreviewer(out, detectGraphics()))
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(out))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "psdish:", err)
		return 1
	}
	return 0
}

func newModel(d *psd.Doc, o options, pv *previewer) *model {
	g := nerdGlyphs
	if o.plain || os.Getenv("PSDISH_PLAIN") != "" {
		g = plainGlyphs
	}
	m := &model{doc: d, opts: o, glyphs: g, collapsed: map[int]bool{}, preview: pv}
	for _, n := range d.Nodes() {
		if n.IsGroup() && !n.FileOpen() {
			m.collapsed[n.ID] = true
		}
	}
	return m
}

func (m *model) Init() tea.Cmd { return nil }

// @region tui:rows -- VISIBLE ROWS + CURSOR

func (m *model) rows() []*psd.Node {
	var out []*psd.Node
	var walk func(n *psd.Node)
	walk = func(n *psd.Node) {
		for _, c := range n.Children {
			out = append(out, c)
			if c.IsGroup() && !m.collapsed[c.ID] {
				walk(c)
			}
		}
	}
	walk(m.doc.Root)
	return out
}

func (m *model) node() *psd.Node {
	if m.cursor == 0 {
		return nil
	}
	return m.doc.Find(m.cursor)
}

// index of the cursor in rows; -1 for the filename row.
func (m *model) cursorRow(rows []*psd.Node) int {
	for i, n := range rows {
		if n.ID == m.cursor {
			return i
		}
	}
	return -1
}

func (m *model) setCursor(n *psd.Node) {
	if n == nil || n.IsRoot() {
		m.cursor = 0
		return
	}
	m.cursor = n.ID
	m.reveal(n)
}

// expands every collapsed ancestor so n is on screen.
func (m *model) reveal(n *psd.Node) {
	for p := n.Parent; p != nil; p = p.Parent {
		delete(m.collapsed, p.ID)
	}
}

func (m *model) moveCursor(delta int) {
	rows := m.rows()
	i := m.cursorRow(rows) + delta
	switch {
	case i < 0:
		m.cursor = 0
	case i < len(rows):
		m.cursor = rows[i].ID
	}
}

// @region tui:update -- KEY HANDLING

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.preview.out != nil {
			m.preview.cellW, m.preview.cellH = cellSize(m.preview.out.f)
		}
	case previewTickMsg:
		cmd = m.preview.start(msg, m.doc.Snapshot())
	case previewDoneMsg:
		cmd = m.handlePreviewDone(msg.content)
	case previewDeleteMsg:
		m.preview.deleteImage(msg.id)
	case tea.KeyMsg:
		switch {
		case m.prompt != nil:
			cmd = m.updatePrompt(msg)
		case m.confirm != nil:
			cmd = m.updateConfirm(msg)
		default:
			m.status = ""
			cmd = m.handleKey(msg)
		}
	default:
		if m.prompt != nil {
			var c tea.Cmd
			m.prompt.input, c = m.prompt.input.Update(msg)
			cmd = c
		}
	}
	return m, tea.Batch(cmd, m.preview.request(m.previewKey()))
}

func (m *model) handlePreviewDone(c *previewContent) tea.Cmd {
	cmd := m.preview.finish(c)
	if c.key == m.previewKey() {
		m.preview.current = c
	}
	return cmd
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	n := m.node()
	switch msg.String() {
	case "q", "ctrl+c":
		if m.doc.Dirty() {
			m.confirm = &confirm{kind: confirmQuit, text: "Quit without saving?"}
			return nil
		}
		return m.quit()
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "left", "h":
		if n != nil {
			m.setCursor(n.Parent)
		}
	case "right", "l":
		if n == nil {
			if rows := m.rows(); len(rows) > 0 {
				m.cursor = rows[0].ID
			}
		} else if n.IsGroup() && len(n.Children) > 0 {
			m.setCursor(n.Children[0])
		}
	case "tab":
		if n != nil && n.IsGroup() {
			m.collapsed[n.ID] = !m.collapsed[n.ID]
		}
	case "ctrl+s":
		return m.save(m.doc.Path)
	case "ctrl+a":
		m.openPrompt(promptSaveAs, "Save as", filepath.Base(m.doc.Path), "")
	case "e":
		name := strings.TrimSuffix(filepath.Base(m.doc.Path), filepath.Ext(m.doc.Path))
		if n != nil {
			name = n.Name()
		}
		m.openPrompt(promptExport, "Export PNG", defaultExportName(name), "> ")
	case "u":
		m.history(m.doc.Undo, "nothing to undo")
	case "U", "ctrl+r":
		m.history(m.doc.Redo, "nothing to redo")
	}
	if n == nil {
		return nil
	}
	switch msg.String() {
	case " ":
		m.edit(m.doc.ToggleVisible(n))
	case "r":
		m.openPrompt(promptRename, "Rename", n.Name(), "")
	case "K":
		m.edit(m.doc.Shift(n, -1))
	case "J":
		m.edit(m.doc.Shift(n, 1))
	case "L":
		m.edit(m.doc.Indent(n))
	case "H":
		m.edit(m.doc.Outdent(n))
	case "g":
		m.openPrompt(promptGroup, "New group", "", "")
	case "d":
		text := fmt.Sprintf("Delete %q?", n.Name())
		if k := n.DescendantCount(); k > 0 {
			text = fmt.Sprintf("Delete %q and %d nested item(s)?", n.Name(), k)
		}
		m.confirm = &confirm{kind: confirmDelete, text: text}
	}
	return nil
}

// records an edit result: bumps rev and keeps the cursor in view.
func (m *model) edit(changed bool) {
	if !changed {
		return
	}
	m.rev++
	if n := m.node(); n != nil {
		m.reveal(n)
	}
}

func (m *model) history(fn func() bool, none string) {
	rows := m.rows()
	row := m.cursorRow(rows)
	if !fn() {
		m.setStatus(none, false)
		return
	}
	m.rev++
	if n := m.node(); n != nil {
		m.reveal(n)
		return
	}
	rows = m.rows()
	switch {
	case len(rows) == 0 || row < 0:
		m.cursor = 0
	default:
		m.cursor = rows[min(row, len(rows)-1)].ID
	}
}

func (m *model) deleteSelected() {
	n := m.node()
	if n == nil {
		return
	}
	var next *psd.Node
	sib := n.Parent.Children
	switch i := n.Index(); {
	case i+1 < len(sib):
		next = sib[i+1]
	case i > 0:
		next = sib[i-1]
	default:
		next = n.Parent
	}
	if m.doc.Delete(n) {
		m.rev++
		m.setCursor(next)
	}
}

func (m *model) save(path string) tea.Cmd {
	if err := m.doc.SaveAs(path, !m.opts.noBackup); err != nil {
		m.setStatus("save failed: "+err.Error(), true)
		return nil
	}
	m.setStatus("saved "+filepath.Base(path), false)
	return nil
}

func (m *model) quit() tea.Cmd {
	m.preview.cleanup()
	return tea.Quit
}

func (m *model) setStatus(s string, isErr bool) {
	m.status, m.isErr = s, isErr
}

// @region tui:modals -- PROMPT + CONFIRM

type promptKind int

const (
	promptRename promptKind = iota
	promptGroup
	promptSaveAs
	promptExport
)

type prompt struct {
	kind  promptKind
	title string
	input textinput.Model
}

type confirmKind int

const (
	confirmDelete confirmKind = iota
	confirmQuit
	confirmOverwriteSave
	confirmOverwriteExport
)

type confirm struct {
	kind confirmKind
	text string
	path string
}

func (m *model) openPrompt(kind promptKind, title, value, label string) {
	in := textinput.New()
	in.Prompt = label
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	in.CharLimit = 255
	m.prompt = &prompt{kind: kind, title: title, input: in}
}

func (m *model) updatePrompt(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.prompt = nil
		return nil
	case "enter":
		p := m.prompt
		m.prompt = nil
		return m.submitPrompt(p.kind, strings.TrimSpace(p.input.Value()))
	}
	var cmd tea.Cmd
	m.prompt.input, cmd = m.prompt.input.Update(msg)
	return cmd
}

func (m *model) submitPrompt(kind promptKind, value string) tea.Cmd {
	if value == "" {
		return nil
	}
	n := m.node()
	switch kind {
	case promptRename:
		if n != nil {
			m.edit(m.doc.Rename(n, value))
		}
	case promptGroup:
		if n != nil && m.doc.Wrap(n, value) != nil {
			m.edit(true)
		}
	case promptSaveAs:
		path := siblingPath(m.doc.Path, value)
		if _, err := os.Stat(path); err == nil {
			m.confirm = &confirm{kind: confirmOverwriteSave, text: fmt.Sprintf("Overwrite %s?", filepath.Base(path)), path: path}
			return nil
		}
		return m.save(path)
	case promptExport:
		path := siblingPath(m.doc.Path, value)
		if _, err := os.Stat(path); err == nil {
			m.confirm = &confirm{kind: confirmOverwriteExport, text: fmt.Sprintf("Overwrite %s?", filepath.Base(path)), path: path}
			return nil
		}
		m.export(path)
	}
	return nil
}

func (m *model) export(path string) {
	n := m.node()
	if n == nil {
		n = m.doc.Root
	}
	if err := exportPNG(m.doc, n, path, true); err != nil {
		m.setStatus("export failed: "+err.Error(), true)
		return
	}
	m.setStatus("exported "+filepath.Base(path), false)
}

func (m *model) updateConfirm(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		c := m.confirm
		m.confirm = nil
		switch c.kind {
		case confirmQuit:
			return m.quit()
		case confirmDelete:
			m.deleteSelected()
		case confirmOverwriteSave:
			return m.save(c.path)
		case confirmOverwriteExport:
			m.export(c.path)
		}
	case "n", "N", "esc":
		m.confirm = nil
	case "ctrl+c":
		if m.confirm.kind == confirmQuit {
			return m.quit()
		}
		m.confirm = nil
	}
	return nil
}

// @region tui:view -- LAYOUT + RENDERING

type layout struct {
	innerW, innerH int
	sideBySide     bool
	treeW, treeH   int
	prevW, prevH   int
}

const (
	headerLines = 2 // title + rule
	footerLines = 1
	captionRows = 1
)

func (m *model) layout() layout {
	l := layout{innerW: max(0, m.w-6), innerH: max(0, m.h-4)}
	body := max(0, l.innerH-headerLines-footerLines)
	// cells are ~twice as tall as wide
	l.sideBySide = m.w > 2*m.h
	if l.sideBySide {
		l.treeW = max(0, (l.innerW-3)/2)
		l.prevW = max(0, l.innerW-3-l.treeW)
		l.treeH, l.prevH = body, body
	} else {
		l.treeW, l.prevW = l.innerW, l.innerW
		l.treeH = max(0, (body-1)/2)
		l.prevH = max(0, body-1-l.treeH)
	}
	return l
}

func (m *model) previewKey() previewKey {
	l := m.layout()
	return previewKey{target: m.cursor, rev: m.rev, w: l.prevW, h: max(0, l.prevH-captionRows)}
}

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	l := m.layout()
	var body string
	tree := m.viewTree(l.treeW, l.treeH)
	prev := m.viewPreview(l.prevW, l.prevH)
	if l.sideBySide {
		divider := dimStyle.Render(strings.TrimRight(strings.Repeat(" │ \n", l.treeH), "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, tree, divider, prev)
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, tree, dimStyle.Render(strings.Repeat("─", l.innerW)), prev)
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		m.viewHeader(l.innerW),
		dimStyle.Render(strings.Repeat("─", l.innerW)),
		body,
		m.viewFooter(l.innerW),
	)
	view := frameStyle(m.w, m.h).Render(content)
	return m.viewModal(view)
}

func (m *model) viewHeader(w int) string {
	name := filepath.Base(m.doc.Path)
	style := titleStyle
	if m.cursor == 0 {
		style = style.Background(selectBgColor)
	}
	s := style.Render(" " + name + " ")
	if m.doc.Dirty() {
		s += warningStyle.Render(" [modified]")
	}
	special := 0
	for _, n := range m.doc.Nodes() {
		if n.Special() != "" {
			special++
		}
	}
	if special > 0 {
		s += warningStyle.Render(fmt.Sprintf("  %s %d special layer(s)", m.glyphs.warn, special))
	}
	return ansi.Truncate(s, w, "…")
}

const keyHints = "space vis · r rename · J/K move · H/L nest · g group · d delete · e export · tab fold · u/U undo/redo · ^s save · ^a save as · q quit"

func (m *model) viewFooter(w int) string {
	switch {
	case m.status != "" && m.isErr:
		return ansi.Truncate(errorStyle.Render(m.status), w, "…")
	case m.status != "":
		return ansi.Truncate(doneStyle.Render(m.status), w, "…")
	}
	return dimStyle.Render(ansi.Truncate(keyHints, w, "…"))
}

func (m *model) viewTree(w, h int) string {
	rows := m.rows()
	cur := m.cursorRow(rows)
	if cur >= 0 {
		if cur < m.scroll {
			m.scroll = cur
		} else if cur >= m.scroll+h {
			m.scroll = cur - h + 1
		}
	}
	m.scroll = max(0, min(m.scroll, len(rows)-h))
	lines := make([]string, 0, h)
	for i := m.scroll; i < len(rows) && len(lines) < h; i++ {
		lines = append(lines, m.viewRow(rows[i], i == cur, w))
	}
	if len(rows) == 0 && h > 0 {
		lines = append(lines, dimStyle.Render("(no layers)"))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lipgloss.NewStyle().Width(w).Render(strings.Join(lines, "\n"))
}

func (m *model) viewRow(n *psd.Node, selected bool, w int) string {
	base := textStyle
	if n.EffectivelyHidden() {
		base = dimStyle
	}
	if selected {
		base = base.Background(selectBgColor)
	}
	vis := m.glyphs.visible
	if n.Hidden() {
		vis = m.glyphs.hidden
	}
	collapsed := m.collapsed[n.ID]
	text := vis + " " + strings.Repeat("  ", n.Depth()) + m.glyphs.icon(n, collapsed) + " " + n.Name()
	s := base.Render(text)
	if n.IsGroup() && collapsed {
		s += base.Foreground(dimColor).Render(fmt.Sprintf(" (%d)", n.DescendantCount()))
	}
	if n.Special() != "" {
		ws := warningStyle
		if selected {
			ws = ws.Background(selectBgColor)
		}
		s += ws.Render(" " + m.glyphs.warn)
	}
	s = ansi.Truncate(s, w, "…")
	if selected {
		if pad := w - lipgloss.Width(s); pad > 0 {
			s += base.Render(strings.Repeat(" ", pad))
		}
	}
	return s
}

func (m *model) viewPreview(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	imgH := max(0, h-captionRows)
	c := m.preview.current
	var lines []string
	caption := ""
	switch {
	case c == nil || c.key != m.previewKey():
		lines = centered(dimStyle.Render("…"), w, imgH)
	case c.message != "":
		lines = centered(dimStyle.Render(c.message), w, imgH)
	default:
		lines = append([]string(nil), c.lines...)
		caption = c.caption
		if c.approx {
			caption += " · approx"
		}
	}
	for len(lines) < imgH {
		lines = append(lines, "")
	}
	lines = append(lines[:imgH], lipgloss.PlaceHorizontal(w, lipgloss.Center, dimStyle.Render(caption)))
	return lipgloss.NewStyle().Width(w).Render(strings.Join(lines, "\n"))
}

func centered(s string, w, h int) []string {
	s = ansi.Truncate(s, w, "…")
	lines := make([]string, h)
	if h > 0 {
		lines[h/2] = lipgloss.PlaceHorizontal(w, lipgloss.Center, s)
	}
	return lines
}

func (m *model) viewModal(bg string) string {
	var box string
	mw := min(60, max(20, m.w-8))
	switch {
	case m.prompt != nil:
		in := m.prompt.input
		in.Width = mw - 6 - lipgloss.Width(in.Prompt)
		box = modalStyle(mw).Render(titleStyle.Render(m.prompt.title) + "\n\n" + in.View() + "\n\n" + dimStyle.Render("enter confirm · esc cancel"))
	case m.confirm != nil:
		box = modalStyle(mw).Render(titleStyle.Render(m.confirm.text) + "\n\n" + dimStyle.Render("y yes · n no"))
	default:
		return bg
	}
	return overlay.Composite(box, bg, overlay.Center, overlay.Center, 0, 0)
}
