package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/tsraveling/psdish/psd"
)

// @region ui:styles -- COLORS + SHARED STYLES

var (
	primaryColor   = lipgloss.Color("206")
	textColor      = lipgloss.Color("254")
	dimColor       = lipgloss.Color("243")
	selectBgColor  = lipgloss.Color("237")
	errorColor     = lipgloss.Color("#cc4444")
	warningColor   = lipgloss.Color("#ccaa22")
	doneColor      = lipgloss.Color("118")
	borderColor    = lipgloss.Color("240")
	previewBgColor = [3]int{30, 30, 30}

	textStyle    = lipgloss.NewStyle().Foreground(textColor)
	dimStyle     = lipgloss.NewStyle().Foreground(dimColor)
	warningStyle = lipgloss.NewStyle().Foreground(warningColor)
	errorStyle   = lipgloss.NewStyle().Foreground(errorColor)
	doneStyle    = lipgloss.NewStyle().Foreground(doneColor)
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(primaryColor)
)

func frameStyle(w, h int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 2).
		Width(w - 2).
		Height(h - 2)
}

func modalStyle(w int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Padding(1, 2).
		Width(w)
}

// @region ui:icons -- GLYPH SETS

type glyphs struct {
	visible, hidden, warn                  string
	groupOpen, groupClosed, layer, special string
	specials                               map[string]string
}

var nerdGlyphs = glyphs{
	visible:     "", // eye
	hidden:      " ",
	warn:        "", // warning
	groupOpen:   "", // open folder
	groupClosed: "", // folder
	layer:       "", // image
	special:     "", // question
	specials: map[string]string{
		"text":         "", // font
		"smart object": "", // cube
		"adjustment":   "", // adjust
		"fill":         "", // tint
	},
}

var plainGlyphs = glyphs{
	visible:     "●",
	hidden:      " ",
	warn:        "!",
	groupOpen:   "□",
	groupClosed: "□",
	layer:       "○",
	special:     "△",
}

func (g glyphs) icon(n *psd.Node, collapsed bool) string {
	switch {
	case n.IsGroup() && collapsed:
		return g.groupClosed
	case n.IsGroup():
		return g.groupOpen
	case n.Special() != "":
		if s, ok := g.specials[n.Special()]; ok {
			return s
		}
		return g.special
	}
	return g.layer
}
