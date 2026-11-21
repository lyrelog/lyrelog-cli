package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Number writes 1234567 as "1,234,567".
func Number(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Compact writes 1234567 as "1.2M", 12400 as "12.4k".
func Compact(n int64) string {
	f := float64(n)
	switch {
	case math.Abs(f) >= 1e9:
		return trimZero(fmt.Sprintf("%.1f", f/1e9)) + "B"
	case math.Abs(f) >= 1e6:
		return trimZero(fmt.Sprintf("%.1f", f/1e6)) + "M"
	case math.Abs(f) >= 1e4:
		return trimZero(fmt.Sprintf("%.1f", f/1e3)) + "k"
	}
	return Number(n)
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// Ago writes how long ago t was, from now: "just now", "42s ago", "5m ago", "3h ago",
// "2d ago", then the date ("Nov 3", or "Nov 3, 2025" in another year).
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < 0 && d > -time.Minute:
		return "just now"
	case d < 0:
		return "in " + Duration(-d)
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// AgoMillis is Ago for Unix milliseconds (0: never).
func AgoMillis(ms int64, now time.Time) string {
	if ms == 0 {
		return "never"
	}
	return Ago(time.UnixMilli(ms), now)
}

// Duration writes a span the way people say it: "850ms", "42s", "3m 20s", "2h 5m", "3d 4h".
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return withRest(int(d.Minutes()), "m", int(d.Seconds())%60, "s")
	case d < 48*time.Hour:
		return withRest(int(d.Hours()), "h", int(d.Minutes())%60, "m")
	}
	return withRest(int(d.Hours()/24), "d", int(d.Hours())%24, "h")
}

func withRest(a int, ua string, b int, ub string) string {
	if b == 0 {
		return fmt.Sprintf("%d%s", a, ua)
	}
	return fmt.Sprintf("%d%s %d%s", a, ua, b, ub)
}

// Clock writes an event's time: "14:03:22" today, "Nov 18 14:03:22" before.
func Clock(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04:05")
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 02 15:04:05")
	}
	return t.Format("2006-01-02 15:04")
}

// Percent writes 0.4123 as "41%" (one decimal under 10%).
func Percent(f float64) string {
	p := f * 100
	if p > 0 && p < 10 {
		return trimZero(fmt.Sprintf("%.1f", p)) + "%"
	}
	return fmt.Sprintf("%.0f%%", p)
}

// Plural picks "1 event" or "3 events".
func Plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return Number(n) + " " + many
}

// Sparkline draws counts as ▁▂▃▄▅▆▇█ (a space for zero).
func Sparkline(values []int64) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	var top int64
	for _, v := range values {
		top = max(top, v)
	}
	var b strings.Builder
	for _, v := range values {
		switch {
		case v <= 0:
			b.WriteRune(' ')
		case top == 0:
			b.WriteRune(bars[0])
		default:
			i := int(math.Ceil(float64(v)/float64(top)*float64(len(bars)))) - 1
			b.WriteRune(bars[min(max(i, 0), len(bars)-1)])
		}
	}
	return b.String()
}

// Bar is a meter of width cells, filled to share (0..1).
func Bar(share float64, width int) (filled, empty string) {
	share = math.Max(0, math.Min(1, share))
	n := int(math.Round(share * float64(width)))
	return strings.Repeat("█", n), strings.Repeat("░", width-n)
}
