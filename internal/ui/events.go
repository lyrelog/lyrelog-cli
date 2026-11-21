package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"go.lyrelog.com/cli/internal/api"
)

// levelWidth: "critical", the longest level name.
const levelWidth = 8

// Columns are the widths an event line pads its message and area to (0: no padding).
type Columns struct{ Message, Area int }

// The widest a message and an area get in a list before they're cut.
const (
	MessageCap = 36
	AreaCap    = 16
)

// ColumnsFor measures a batch of events so their messages and areas line up (capped, so one
// long name doesn't push everything right).
func ColumnsFor(events []api.Event) Columns {
	var c Columns
	for _, e := range events {
		c = c.Widen(e)
	}
	return c
}

// Widen makes room for one more event's message and area, up to the caps (--follow, where
// lines come one at a time).
func (c Columns) Widen(e api.Event) Columns {
	message := e.Message
	if message == "" {
		message = "(no message)"
	}
	c.Message = max(c.Message, min(Width(message), MessageCap))
	c.Area = max(c.Area, min(Width(e.Category), AreaCap))
	return c
}

// EventLine is one event as the explorer lists it: time, a bar in the level's colour, the
// level, the message, the area dimmed, then a preview of the attributes, fitted to width.
func EventLine(th Theme, e api.Event, now time.Time, width int) string {
	return EventLineIn(th, e, now, width, Columns{})
}

// EventLineIn is EventLine with the message and area padded to cols.
func EventLineIn(th Theme, e api.Event, now time.Time, width int, cols Columns) string {
	clock := Clock(time.UnixMilli(e.TS).Local(), now)
	level := e.Level
	if level == "" {
		level = "info"
	}
	head := th.Dim(clock) + " " + th.LevelFill(level, "▌") + th.Level(level, fmt.Sprintf("%-*s", levelWidth, level)) + " "
	message := e.Message
	if message == "" {
		message = "(no message)"
	}
	pad := func(s string, w int) string {
		if w == 0 {
			return ""
		}
		return strings.Repeat(" ", max(w-Width(s), 0))
	}
	if cols.Message > 0 && Width(message) > cols.Message {
		message = Truncate(message, cols.Message)
	}
	area := e.Category
	if cols.Area > 0 && Width(area) > cols.Area {
		area = Truncate(area, cols.Area)
	}
	line := head + th.Bold(message) + pad(message, cols.Message)
	if area != "" || cols.Area > 0 {
		line += "  " + th.Dim(area) + pad(area, cols.Area)
	}
	if preview := AttrsPreview(e.Attrs); preview != "" {
		line += "  " + th.Dim(preview)
	}
	line = strings.TrimRight(line, " ")
	if width > 0 && Width(line) > width {
		line = Truncate(line, width)
	}
	return line
}

// AttrsPreview writes attributes as key=value pairs, sorted, nested ones flattened with dots.
func AttrsPreview(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var attrs map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&attrs) != nil || len(attrs) == 0 {
		return ""
	}
	var pairs []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(prefix+k+".", x[k])
			}
		default:
			pairs = append(pairs, strings.TrimSuffix(prefix, ".")+"="+scalar(x))
		}
	}
	walk("", attrs)
	return strings.Join(pairs, " ")
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" || strings.ContainsAny(x, " \t\n=\"") {
			b, _ := json.Marshal(x)
			return string(b)
		}
		return x
	case nil:
		return "null"
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// EventDetail writes one event in full: its message, the fields that are set, the attributes and
// the payload as indented JSON. person is the project's word for one of its people.
func EventDetail(w io.Writer, th Theme, e api.Event, now time.Time, person string) {
	level := e.Level
	if level == "" {
		level = "info"
	}
	fmt.Fprintf(w, "%s %s  %s\n", th.LevelFill(level, "▌"), th.Bold(e.Message), th.Level(level, level))
	if e.Category != "" {
		fmt.Fprintf(w, "  %s\n", th.Dim(e.Category))
	}
	fmt.Fprintln(w)
	at := time.UnixMilli(e.TS).Local()
	fields := [][2]string{
		{"Time", at.Format("2006-01-02 15:04:05.000 MST") + th.Dim("  "+Ago(at, now))},
		{"Event", e.EventID},
		{Capitalize(person), e.UserID},
		{"Session", e.SessionID},
		{"Install", e.InstallID},
		{"App", strings.TrimSpace(e.AppID + " " + version(e.AppVersion, e.AppBuild))},
		{"Device", strings.TrimSpace(e.DeviceModel + " " + th.Dim(strings.TrimSpace(e.OSName+" "+e.OSVersion)))},
		{"Locale", e.Locale},
		{"Country", e.Country},
		{"Environment", e.Environment},
		{"Platform", e.Platform},
		{"Verified", verified(e.VerifiedBy)},
	}
	for _, f := range fields {
		if strings.TrimSpace(f[1]) == "" {
			continue
		}
		fmt.Fprintf(w, "  %s %s\n", th.Dim(fmt.Sprintf("%-12s", f[0])), f[1])
	}
	if len(e.Attrs) > 0 && string(e.Attrs) != "{}" && string(e.Attrs) != "null" {
		fmt.Fprintf(w, "\n%s\n", th.Bold("Attributes"))
		PrettyJSON(w, th, e.Attrs, "  ")
	}
	if len(e.Payload) > 0 && string(e.Payload) != "null" {
		fmt.Fprintf(w, "\n%s\n", th.Bold("Payload"))
		PrettyJSON(w, th, e.Payload, "  ")
	}
}

func version(v, build string) string {
	switch {
	case v == "":
		return build
	case build == "" || build == v:
		return v
	}
	return v + " (" + build + ")"
}

func verified(by string) string {
	switch by {
	case "":
		return ""
	case "server":
		return "by a server key"
	case "app_attest":
		return "by Apple App Attest"
	}
	return by
}

// PrettyJSON indents JSON, keys in cobalt, strings in green, numbers and literals in amber.
func PrettyJSON(w io.Writer, th Theme, raw json.RawMessage, prefix string) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, prefix, "  "); err != nil {
		fmt.Fprintln(w, prefix+string(raw))
		return
	}
	text := prefix + buf.String()
	if !th.Color {
		fmt.Fprintln(w, text)
		return
	}
	fmt.Fprintln(w, colorJSON(th, text))
}

// colorJSON colours indented JSON token by token (strings never hold raw newlines).
func colorJSON(th Theme, text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(text) && text[j] != '"' {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(text))
			token := text[i:j]
			k := j
			for k < len(text) && text[k] == ' ' {
				k++
			}
			if k < len(text) && text[k] == ':' {
				out.WriteString(th.Primary(token))
			} else {
				out.WriteString(th.Success(token))
			}
			i = j
		case c == '-' || (c >= '0' && c <= '9') || c == 't' || c == 'f' || c == 'n':
			j := i
			for j < len(text) && !strings.ContainsRune(",]}\n ", rune(text[j])) {
				j++
			}
			out.WriteString(th.Warning(text[i:j]))
			i = j
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}
