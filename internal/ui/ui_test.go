package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.lyrelog.com/cli/internal/api"
)

var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	time.Local = time.UTC
	m.Run()
}

func TestNumbers(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4200: "-4,200"} {
		if got := Number(in); got != want {
			t.Errorf("Number(%d) = %q", in, got)
		}
	}
	for in, want := range map[int64]string{950: "950", 9999: "9,999", 12400: "12.4k", 1_000_000: "1M", 1_260_000: "1.3M", 3_000_000_000: "3B"} {
		if got := Compact(in); got != want {
			t.Errorf("Compact(%d) = %q, want %q", in, got, want)
		}
	}
	if Percent(0.4123) != "41%" || Percent(0.0512) != "5.1%" || Percent(1) != "100%" {
		t.Error(Percent(0.4123), Percent(0.0512), Percent(1))
	}
}

func TestTimes(t *testing.T) {
	cases := map[time.Duration]string{2 * time.Second: "just now", 42 * time.Second: "42s ago", 5 * time.Minute: "5m ago",
		3 * time.Hour: "3h ago", 49 * time.Hour: "2d ago", 20 * 24 * time.Hour: "Oct 30", -30 * time.Second: "just now"}
	for d, want := range cases {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("Ago(-%v) = %q, want %q", d, got, want)
		}
	}
	if Ago(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), now) != "Mar 1, 2025" {
		t.Error(Ago(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), now))
	}
	for d, want := range map[time.Duration]string{850 * time.Millisecond: "850ms", 42 * time.Second: "42s", 200 * time.Second: "3m 20s",
		2*time.Hour + 5*time.Minute: "2h 5m", 76 * time.Hour: "3d 4h", time.Hour: "1h"} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
	if Clock(now.Add(-time.Hour), now) != "11:00:00" || Clock(now.Add(-25*time.Hour), now) != "Nov 18 11:00:00" {
		t.Error(Clock(now.Add(-time.Hour), now), Clock(now.Add(-25*time.Hour), now))
	}
}

func TestSparkline(t *testing.T) {
	if got := Sparkline([]int64{0, 1, 4, 8}); got != " ▁▄█" {
		t.Errorf("%q", got)
	}
}

func TestTableFitsTheWidth(t *testing.T) {
	tb := Table{Columns: []Column{{Header: "Name", Flex: true}, {Header: "Count", Right: true}, {Header: "Where"}}}
	tb.Add("a very long message that will not fit in a narrow terminal", "1,234", "checkout")
	tb.Add("short", "5", "home")
	var buf bytes.Buffer
	tb.Render(&buf, Theme{}, 40)
	want := "Name                     Count  Where\n" +
		"a very long message th…  1,234  checkout\n" +
		"short                        5  home\n"
	if buf.String() != want {
		t.Errorf("got\n%s\nwant\n%s", buf.String(), want)
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if Width(line) > 40 {
			t.Errorf("line wider than 40: %q", line)
		}
	}
}

func TestTableNeverCutsKeptColumns(t *testing.T) {
	tb := Table{Columns: []Column{{Header: "ID", Keep: true}, {Header: "Name", Flex: true, Keep: true}, {Header: "Where", Flex: true}, {Header: "Device"}}}
	tb.Add("0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40", "tilefall-api-production", "app.opened → purchase.failed", "iPhone17,1")
	var buf bytes.Buffer
	tb.Render(&buf, Theme{}, 60)
	row := strings.Split(buf.String(), "\n")[1]
	if !strings.Contains(row, "0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40") || !strings.Contains(row, "tilefall-api-production") {
		t.Errorf("a kept column was cut: %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("the others weren't cut first: %q", row)
	}
}

func TestEventLine(t *testing.T) {
	e := api.Event{TS: now.Add(-90 * time.Second).UnixMilli(), Level: "error", Category: "checkout", Message: "payment.failed",
		Attrs: json.RawMessage(`{"amount":12.5,"card":{"brand":"visa"},"note":"two words"}`)}
	got := EventLine(Theme{}, e, now, 0)
	want := `11:58:30 ▌error    payment.failed  checkout  amount=12.5 card.brand=visa note="two words"`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if cut := EventLine(Theme{}, e, now, 40); Width(cut) != 40 || !strings.HasSuffix(cut, "…") {
		t.Errorf("not cut to 40: %q", cut)
	}
	colored := EventLine(Theme{Color: true}, e, now, 0)
	if !strings.Contains(colored, "\x1b[") {
		t.Error("no colour with Color on")
	}
	// The error level is the dashboard's red text colour (#d61e24 on light terminals).
	if !strings.Contains(colored, "214;30;36") {
		t.Errorf("error level not in the dashboard's red: %q", colored)
	}
}

func TestNoColorHasNoEscapes(t *testing.T) {
	th := Theme{}
	for _, s := range []string{th.Bold("x"), th.Dim("x"), th.Primary("x"), th.Level("error", "x"), th.Mark("ok"), th.Error("x")} {
		if strings.Contains(s, "\x1b") {
			t.Errorf("escape code without colour: %q", s)
		}
	}
	var buf bytes.Buffer
	PrettyJSON(&buf, th, json.RawMessage(`{"a":1,"b":"x"}`), "")
	if strings.Contains(buf.String(), "\x1b") || !strings.Contains(buf.String(), `"a": 1`) {
		t.Errorf("%q", buf.String())
	}
}

func TestColorJSON(t *testing.T) {
	out := colorJSON(Theme{Color: true}, `{"key": "value", "n": -1.5, "ok": true, "s": "a \"q\" b"}`)
	if !strings.Contains(out, "value") || !strings.Contains(out, `a \"q\" b`) || !strings.Contains(out, "-1.5") {
		t.Errorf("%q", out)
	}
}

// When the flexible columns are at their narrowest and the table still doesn't fit, it leaves
// out the columns that can go (Drop, lowest first) rather than cut every short one to "Sta…":
// the sessions table at 80 and 100 columns, with a real session id.
func TestTableDropsColumnsBeforeCuttingShortOnes(t *testing.T) {
	cols := func() []Column {
		return []Column{{Header: "Started"}, {Header: "Person", Keep: true}, {Header: "Length", Right: true}, {Header: "Events", Right: true, Drop: 4},
			{Header: "Errors", Right: true}, {Header: "Device", Flex: true, Drop: 1}, {Header: "Version", Drop: 2}, {Header: "From → to", Flex: true, Min: 12, Drop: 3}, {Header: "Session", Keep: true}}
	}
	for _, width := range []int{80, 100, 120} {
		tb := Table{Columns: cols()}
		tb.Add("16m ago", "pl-c0bae756", "13m 37s", "17", "0", "iPad16,6", "1.6.3", "app_launch → game_started", "3cb32a5b-e91e-4770-8222-43a08cc6e107")
		var buf bytes.Buffer
		tb.Render(&buf, Theme{}, width)
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		for _, l := range lines {
			if Width(l) > width {
				t.Errorf("%d: wider than the terminal: %q", width, l)
			}
		}
		if strings.Contains(lines[0], "…") || !strings.HasPrefix(lines[1], "16m ago") || !strings.Contains(lines[1], "3cb32a5b-e91e-4770-8222-43a08cc6e107") {
			t.Errorf("%d: short columns were cut instead of dropped:\n%s", width, buf.String())
		}
	}
	// Wide enough: nothing is left out.
	tb := Table{Columns: cols()}
	tb.Add("16m ago", "pl-c0bae756", "13m 37s", "17", "0", "iPad16,6", "1.6.3", "app_launch → game_started", "3cb32a5b-e91e-4770-8222-43a08cc6e107")
	var buf bytes.Buffer
	tb.Render(&buf, Theme{}, 160)
	if !strings.Contains(buf.String(), "iPad16,6") || !strings.Contains(buf.String(), "game_started") {
		t.Errorf("dropped with room to spare:\n%s", buf.String())
	}
}
