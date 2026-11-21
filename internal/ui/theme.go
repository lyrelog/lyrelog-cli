// Package ui is how the CLI looks to people: Lyrelog's colours, tables, event lines, a spinner,
// relative times. Nothing here is used in JSON mode except the plain formatters.
package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Palette: the dashboard's "Studio" tokens (../lyrelog/web/src/index.css). On a light terminal
// the -text variants (AA on near-white), on a dark one the dark theme's.
type swatch struct{ light, dark string }

var (
	cobalt = swatch{"#2b59ff", "#6d8bff"}
	levels = map[string]swatch{
		"trace":    {"#646e7f", "#8d94a1"},
		"debug":    {"#656f7e", "#8d94a1"},
		"info":     {"#2b59ff", "#6d8bff"},
		"notice":   {"#0c7c5e", "#3fcf9f"},
		"warning":  {"#996100", "#f2b23e"},
		"error":    {"#d61e24", "#ff6b6f"},
		"critical": {"#b4235a", "#ff5c9a"},
	}
	// The plain fills, for bars and dots (as in the dashboard's charts).
	fills = map[string]swatch{
		"trace":    {"#c3c8d0", "#4a505b"},
		"debug":    {"#a7aeb9", "#5d6573"},
		"info":     {"#2b59ff", "#6d8bff"},
		"notice":   {"#0f9d76", "#3fcf9f"},
		"warning":  {"#d98a00", "#f2b23e"},
		"error":    {"#e5484d", "#ff6b6f"},
		"critical": {"#b4235a", "#ff5c9a"},
	}
	muted = swatch{"#5b6472", "#8d94a1"}
)

// Theme renders text; with colour off every style is a no-op, so output has no escape codes.
type Theme struct {
	Color bool
	Dark  bool
}

func (t Theme) pick(s swatch) color.Color {
	if t.Dark {
		return lipgloss.Color(s.dark)
	}
	return lipgloss.Color(s.light)
}

func (t Theme) style() lipgloss.Style { return lipgloss.NewStyle() }

func (t Theme) render(s lipgloss.Style, text string) string {
	if !t.Color || text == "" {
		return text
	}
	return s.Render(text)
}

// Bold is for headings and names.
func (t Theme) Bold(s string) string { return t.render(t.style().Bold(true), s) }

// Dim is for what's secondary: areas, ids, times.
func (t Theme) Dim(s string) string { return t.render(t.style().Foreground(t.pick(muted)), s) }

// Primary is cobalt: what you act on (commands to run, links).
func (t Theme) Primary(s string) string { return t.render(t.style().Foreground(t.pick(cobalt)), s) }

// PrimaryBold is cobalt and bold.
func (t Theme) PrimaryBold(s string) string {
	return t.render(t.style().Foreground(t.pick(cobalt)).Bold(true), s)
}

// Level colours text with a level's text colour.
func (t Theme) Level(level, s string) string {
	sw, ok := levels[level]
	if !ok {
		return s
	}
	st := t.style().Foreground(t.pick(sw))
	if level == "error" || level == "critical" {
		st = st.Bold(true)
	}
	return t.render(st, s)
}

// LevelFill colours a bar or dot with the level's fill.
func (t Theme) LevelFill(level, s string) string {
	sw, ok := fills[level]
	if !ok {
		return t.Dim(s)
	}
	return t.render(t.style().Foreground(t.pick(sw)), s)
}

// Error is red text (only for errors).
func (t Theme) Error(s string) string { return t.Level("error", s) }

// Success is the notice green.
func (t Theme) Success(s string) string {
	return t.render(t.style().Foreground(t.pick(levels["notice"])), s)
}

// Warning is amber.
func (t Theme) Warning(s string) string {
	return t.render(t.style().Foreground(t.pick(levels["warning"])), s)
}

// Command shows something to type: cobalt on a terminal, in backticks without colour.
func (t Theme) Command(s string) string {
	if !t.Color {
		return s
	}
	return t.Primary(s)
}

// Mark is a status mark: ✓ in green, ✗ in red, • dim.
func (t Theme) Mark(kind string) string {
	switch kind {
	case "ok":
		return t.Success("✓")
	case "fail":
		return t.Error("✗")
	default:
		return t.Dim("•")
	}
}

// Capitalize upper-cases the first letter ("no such project" → "No such project").
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
