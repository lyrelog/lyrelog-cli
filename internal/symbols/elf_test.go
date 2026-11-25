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

// A Flutter build's Dart files are named as Dart names them when they're the SDK's, the engine's
// or a package's, and by their base name when they're the app's (as the build machine's folders
// never leave it).
func TestDartFilesAreNamedAsDartNamesThem(t *testing.T) {
	for in, want := range map[string]string{
		"third_party/dart/sdk/lib/async/future.dart":                "dart:async/future.dart",
		"third_party/dart/sdk/lib/_internal/vm/lib/timer_impl.dart": "dart:_internal/timer_impl.dart",
		"lib/ui/hooks.dart": "dart:ui/hooks.dart",
		"/opt/flutter/packages/flutter/lib/src/widgets/framework.dart":                              "package:flutter/src/widgets/framework.dart",
		"/home/someone/.pub-cache/hosted/pub.dev/lyrelog-1.0.3/lib/lyrelog.dart":                    "package:lyrelog/lyrelog.dart",
		"/home/someone/tilefall/lib/board.dart":                                                     "board.dart",
		"/home/someone/.pub-cache/hosted/pub.dev/lyrelog-1.0.0-beta.1/lib/lyrelog.dart":             "package:lyrelog/lyrelog.dart",
		`C:\Users\someone\AppData\Local\Pub\Cache\hosted\pub.dev\lyrelog-1.0.4\lib\src\errors.dart`: "package:lyrelog/src/errors.dart",
		`C:\Users\someone\tilefall\lib\board.dart`:                                                  "board.dart",
	} {
		if got := DartFile(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
