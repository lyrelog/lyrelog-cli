package cli

import (
	"encoding/json"
	"testing"
)

// A saved search fills what the command line left out, flags winning; its top bar's environment
// and platform come with it, and "every environment" stays every one.
func TestASavedSearchFillsWhatTheFlagsLeaveOut(t *testing.T) {
	f := filters{level: "critical"}
	config := json.RawMessage(`{"range":"7d","level":"error","category":"checkout","attr":["payment.method:card"],"env":"","platform_shown":"ios"}`)
	if err := f.fromView(config); err != nil {
		t.Fatal(err)
	}
	if f.since != "7d" || f.level != "critical" || f.category != "checkout" || f.env != "all" || f.platform != "ios" ||
		len(f.attrs) != 1 || f.attrs[0] != "payment.method:card" {
		t.Fatalf("filled: %+v", f)
	}
	g := filters{since: "1h"}
	if err := g.fromView(json.RawMessage(`{"from":1795000000000,"to":1795003600000,"env":"testflight"}`)); err != nil {
		t.Fatal(err)
	}
	if g.since != "1h" || g.from != "" || g.env != "testflight" {
		t.Fatalf("a window given on the command line was replaced: %+v", g)
	}
	h := filters{}
	_ = h.fromView(json.RawMessage(`{"from":1795000000000,"to":1795003600000}`))
	if h.from != "1795000000000" || h.to != "1795003600000" || h.env != "" {
		t.Fatalf("a fixed window: %+v", h)
	}
}
