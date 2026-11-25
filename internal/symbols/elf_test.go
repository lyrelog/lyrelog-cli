package symbols

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// testdata/native.so is testdata/native.cpp built for Android with the NDK (28.2):
//
//	aarch64-linux-android24-clang++ -O2 -g -shared -fPIC -fdebug-prefix-map=<dir>=/tilefall \
//	    -Wl,--build-id=sha1 native.cpp -o native.so
//
// and testdata/native.symbolizer.txt is what the NDK's llvm-symbolizer --inlining answers for
// each of its instructions.
func TestANativeLibraryReadsAsLLVMSymbolizerReadsIt(t *testing.T) {
	f, err := os.Open("testdata/native.so")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := ExtractELF(f, "libtilefall.so")
	if err != nil {
		t.Fatal(err)
	}
	if m.UUID != "0ebb2808997eeafc84f0fcfb81b4cee0c2b53f68" || m.Arch != "arm64" || m.TextAddr != 0 {
		t.Fatalf("%s %s %#x", m.UUID, m.Arch, m.TextAddr)
	}
	if _, err := exec.LookPath("c++filt"); err != nil {
		if _, err := exec.LookPath("xcrun"); err != nil {
			t.Skip("no c++filt")
		}
	}
	if err := Demangle(context.Background(), []*Map{m}); err != nil {
		t.Fatal(err)
	}
	want, err := os.Open("testdata/native.symbolizer.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer want.Close()
	type c struct {
		addr   uint64
		frames []string
	}
	var cases []c
	sc := bufio.NewScanner(want)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "0x"):
			a, err := strconv.ParseUint(line, 0, 64)
			if err != nil {
				t.Fatal(err)
			}
			cases = append(cases, c{addr: a})
		default:
			if line == " (:0)" {
				continue // the PLT: nothing has a name there
			}
			cases[len(cases)-1].frames = append(cases[len(cases)-1].frames, line)
		}
	}
	if len(cases) < 90 {
		t.Fatalf("only %d addresses", len(cases))
	}
	for _, w := range cases {
		var got []string
		for _, fr := range m.Lookup(w.addr) {
			file := fr.File
			if file == "" && fr.Line == 0 {
				file = "crtbegin_so.c" // the NDK's startup code: a symbol, no debug information
			}
			got = append(got, fmt.Sprintf("%s (%s:%d)", fr.Function, file, fr.Line))
		}
		if strings.Join(got, "\n") != strings.Join(w.frames, "\n") {
			t.Errorf("%#x:\n got  %q\n want %q", w.addr, got, w.frames)
		}
	}
}

func TestAStrippedLibraryIsTheLeastRich(t *testing.T) {
	f, err := os.Open("testdata/native.so")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if id, rich := Richness(f); id != "0ebb2808997eeafc84f0fcfb81b4cee0c2b53f68" || rich != 2 {
		t.Fatalf("%s %d", id, rich)
	}
}
