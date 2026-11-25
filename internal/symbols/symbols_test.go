package symbols

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/app.symbols.json from testdata/app.dwarf (needs Xcode)")

// testdata/app.dwarf is the dSYM of testdata/app.swift, built with
//
//	swiftc -O -g -c main.swift -module-name app -debug-prefix-map <dir>=/app && swiftc main.o -o app && dsymutil app
//
// (optimized: functions inlined, generics specialized). testdata/app.symbols.json is its map,
// demangled: the golden file Lyrelog's server tests its own lookup against.
func golden(t *testing.T) *Map {
	t.Helper()
	data, err := os.ReadFile("testdata/app.symbols.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Map
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

// The map written from the dSYM is the golden file (demangling needs Xcode, so this part runs on
// a Mac only).
func TestTheMapOfTheTestAppIsTheGoldenOne(t *testing.T) {
	if _, err := exec.LookPath("xcrun"); err != nil {
		t.Skip("no xcrun: demangling needs Xcode")
	}
	maps, err := Extract("testdata/app.dwarf")
	if err != nil {
		t.Fatal(err)
	}
	if len(maps) != 1 || maps[0].Arch != "arm64" || maps[0].UUID != "A3F11B8C-0F44-3ABF-A3AA-25AC3AF39B8F" || maps[0].TextAddr != 0x100000000 {
		t.Fatalf("maps: %d, first %s %s %#x", len(maps), maps[0].Arch, maps[0].UUID, maps[0].TextAddr)
	}
	if err := Demangle(context.Background(), maps); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(maps[0])
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("testdata/app.symbols.json", append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/app.symbols.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatal("the map differs from testdata/app.symbols.json (go test -run Golden -update rewrites it: review the diff)")
	}
}

var atosLine = regexp.MustCompile(`^(.*) \(in [^)]*\) \((.*):(\d+)\)$`)

// For every instruction of the test app, the lookup answers what Apple's atos -i answers: the
// same functions, inlined ones first, each with its file and line (testdata/app.atos.txt).
func TestLookupAnswersWhatAtosAnswers(t *testing.T) {
	m := golden(t)
	f, err := os.Open("testdata/app.atos.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type want struct {
		addr   uint64
		frames []string
	}
	var cases []want
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#") || line == "":
		case strings.HasPrefix(line, "0x"):
			a, err := strconv.ParseUint(line, 0, 64)
			if err != nil {
				t.Fatal(err)
			}
			cases = append(cases, want{addr: a})
		default:
			c := &cases[len(cases)-1]
			if g := atosLine.FindStringSubmatch(line); g != nil {
				file := g[2]
				file = file[strings.LastIndex(file, "/")+1:]
				c.frames = append(c.frames, fmt.Sprintf("%s (%s:%s)", g[1], file, g[3]))
			} else {
				c.frames = append(c.frames, line)
			}
		}
	}
	if len(cases) < 200 {
		t.Fatalf("only %d addresses read", len(cases))
	}
	for _, c := range cases {
		var got []string
		for _, fr := range m.Lookup(c.addr - m.TextAddr) {
			got = append(got, fmt.Sprintf("%s (%s:%d)", fr.Function, fr.File, fr.Line))
		}
		if strings.Join(got, "\n") != strings.Join(c.frames, "\n") {
			t.Errorf("%#x:\n got  %q\n want %q", c.addr, got, c.frames)
		}
	}
}

func TestAnOffsetOutsideEveryFunctionIsNothing(t *testing.T) {
	m := golden(t)
	for _, off := range []uint64{0, m.TextSize + 4, 1 << 40} {
		if got := m.Lookup(off); got != nil {
			t.Fatalf("%#x: %v", off, got)
		}
	}
}
