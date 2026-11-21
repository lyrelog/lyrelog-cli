package ui

import (
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Column is one column of a table.
type Column struct {
	Header string
	Right  bool // numbers align right
	// Flex columns give up width first when the terminal is narrow (messages, names).
	Flex bool
	// Min is the narrowest a flex column gets (default 8).
	Min int
	// Keep columns are never cut: ids and names people copy (a session, a key). When the
	// table still doesn't fit, it runs past the width rather than cut them.
	Keep bool
	// Drop: when the flex columns are at their narrowest and the table still doesn't fit, the
	// columns with a Drop are left out, 1 first, then 2..., before any other column is cut.
	// 0: never left out.
	Drop int
}

// Table is rows of cells, already styled; widths are measured without escape codes.
type Table struct {
	Columns []Column
	Rows    [][]string
}

const gap = "  "

// Add appends a row.
func (t *Table) Add(cells ...string) { t.Rows = append(t.Rows, cells) }

// Render writes the table, fitted to width (0: no limit).
func (t *Table) Render(w io.Writer, th Theme, width int) {
	n := len(t.Columns)
	widths := make([]int, n)
	for i, c := range t.Columns {
		widths[i] = ansi.StringWidth(c.Header)
	}
	for _, row := range t.Rows {
		for i := 0; i < n && i < len(row); i++ {
			widths[i] = max(widths[i], ansi.StringWidth(row[i]))
		}
	}
	shown := make([]int, 0, n) // the columns drawn, in order
	if width > 0 {
		var hidden []bool
		widths, hidden = fit(t.Columns, widths, width)
		for i := range n {
			if !hidden[i] {
				shown = append(shown, i)
			}
		}
	} else {
		for i := range n {
			shown = append(shown, i)
		}
	}
	line := func(cells []string, header bool) {
		var b strings.Builder
		cell := func(i int) string {
			if i < len(cells) {
				return cells[i]
			}
			return ""
		}
		// Trailing empty cells are dropped, and so is the padding after the last cell.
		last := len(shown) - 1
		for last > 0 && cell(shown[last]) == "" {
			last--
		}
		for k := 0; k <= last; k++ {
			i := shown[k]
			c := cell(i)
			if ansi.StringWidth(c) > widths[i] {
				c = ansi.Truncate(c, widths[i], "…")
			}
			pad := strings.Repeat(" ", max(widths[i]-ansi.StringWidth(c), 0))
			if header {
				c = th.Dim(c)
			}
			switch {
			case t.Columns[i].Right:
				b.WriteString(pad + c)
			case k == last:
				b.WriteString(c)
			default:
				b.WriteString(c + pad)
			}
			if k < last {
				b.WriteString(gap)
			}
		}
		io.WriteString(w, strings.TrimRight(b.String(), " ")+"\n")
	}
	headers := make([]string, n)
	for i, c := range t.Columns {
		headers[i] = c.Header
	}
	line(headers, true)
	for _, row := range t.Rows {
		line(row, false)
	}
}

// fit narrows columns until the table fits: flex columns first, widest first, down to their
// minimum; then it leaves out the Drop columns (1 first), each time starting again from the
// natural widths so the others get the room back; then any left-aligned column down to 4. Keep
// columns are never narrowed.
func fit(cols []Column, natural []int, width int) (widths []int, hidden []bool) {
	hidden = make([]bool, len(cols))
	for {
		widths = append(widths[:0], natural...)
		for i := range widths {
			if hidden[i] {
				widths[i] = 0
			}
		}
		total := func() int {
			sum := 0
			for i, w := range widths {
				if !hidden[i] {
					sum += w + len(gap)
				}
			}
			return sum - len(gap)
		}
		shrink := func(eligible func(i int) (bool, int)) {
			for total() > width {
				widest, at := -1, 0
				for i := range widths {
					if hidden[i] {
						continue
					}
					ok, floor := eligible(i)
					if ok && widths[i] > floor && widths[i] > widest {
						widest, at = widths[i], i
					}
				}
				if widest < 0 {
					return
				}
				widths[at]--
			}
		}
		shrink(func(i int) (bool, int) {
			floor := cols[i].Min
			if floor == 0 {
				floor = 8
			}
			return cols[i].Flex && !cols[i].Keep, max(floor, ansi.StringWidth(cols[i].Header))
		})
		if total() <= width {
			return widths, hidden
		}
		next := -1
		for i, c := range cols {
			if !hidden[i] && c.Drop > 0 && (next < 0 || c.Drop < cols[next].Drop) {
				next = i
			}
		}
		if next < 0 {
			shrink(func(i int) (bool, int) { return !cols[i].Right && !cols[i].Keep, 4 })
			return widths, hidden
		}
		hidden[next] = true
	}
}

// Truncate cuts s to width cells with an ellipsis.
func Truncate(s string, width int) string {
	if width <= 0 || ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// Width is s's width in cells, escape codes ignored.
func Width(s string) int { return ansi.StringWidth(s) }
