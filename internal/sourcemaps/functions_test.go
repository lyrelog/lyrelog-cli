package sourcemaps

import (
	"encoding/json"
	"os"
	"testing"
)

// innermost is the name of the innermost function holding a position, as the server reads the
// table; ok is false outside every function.
func innermost(t *Functions, line, col uint32) (string, bool) {
	p := pos{line, col}
	best := -1
	for i, r := range t.Ranges {
		if !before(p, pos{r[0], r[1]}) && before(p, pos{r[2], r[3]}) {
			best = i // sorted outer first: the last holding it is the innermost
		}
	}
	if best < 0 {
		return "", false
	}
	return t.Names[t.Ranges[best][4]], true
}

func readFunctionsOf(t *testing.T, dir string) *Functions {
	t.Helper()
	bundle, err := os.ReadFile("testdata/tilefall/" + dir + "/main.js")
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile("testdata/tilefall/" + dir + "/main.js.map")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(text, &raw); err != nil {
		t.Fatal(err)
	}
	named := map[pos]string{}
	var shrunk int
	if err := cleanMap(raw, "", named, 0, 0, &shrunk); err != nil {
		t.Fatal(err)
	}
	fns, err := ReadFunctions(bundle, named)
	if err != nil {
		t.Fatal(err)
	}
	return fns
}

// Every frame Node printed for the docs' Tilefall, built minified by esbuild and by Rolldown
// (testdata/tilefall: src/main.ts run with node; positions as the stacks print them), falls in the
// function the source names: "n.weight" is Board.weight, "o" is dropTile, "Object.onTap" is
// rules.onTap; an arrow passed along is anonymous, and the module's own code is in none.
func TestTheBundlesFunctionsAreNamedAsInTheSource(t *testing.T) {
	type frame struct {
		line, col uint32 // 1-based, as printed
		name      string
		in        bool
	}
	for dir, frames := range map[string][]frame{
		"esbuild": {
			{1, 139, "Board.weight", true}, {1, 187, "Board.score", true}, {1, 529, "dropTile", true},
			{1, 653, "rules.onTap", true}, {1, 831, "inner", true}, {1, 1019, "play", true},
			{1, 1126, "", false}, {1, 380, "", true}, {1, 327, "Board.parse", true}, {1, 861, "inner", true},
			{1, 487, "", true}, {1, 472, "Board.labels", true}, {1, 930, "inner", true},
			{1, 582, "rules.check", true}, {1, 1001, "inner", true}, {1, 742, "loadLevel", true},
		},
		"rolldown": {
			{1, 122, "Board.weight", true}, {1, 170, "Board.score", true}, {1, 508, "dropTile", true},
			{1, 600, "rules.onTap", true}, {1, 773, "inner", true}, {1, 961, "play", true},
			{1, 1070, "", false}, {1, 363, "", true}, {1, 310, "Board.parse", true}, {1, 803, "inner", true},
			{1, 466, "", true}, {1, 451, "Board.labels", true}, {1, 872, "inner", true},
			{1, 563, "rules.check", true}, {1, 943, "inner", true}, {1, 685, "loadLevel", true},
		},
	} {
		t.Run(dir, func(t *testing.T) {
			fns := readFunctionsOf(t, dir)
			for _, f := range frames {
				name, in := innermost(fns, f.line-1, f.col-1)
				if name != f.name || in != f.in {
					t.Errorf("%d:%d: %q (in a function: %v), want %q (%v)", f.line, f.col, name, in, f.name, f.in)
				}
			}
		})
	}
}

// Columns count UTF-16 code units, as stacks do: an emoji before a position on its line counts
// two, a character of the BMP one, whatever their UTF-8 length.
func TestColumnsCountUTF16Units(t *testing.T) {
	p := advance(pos{}, []byte("a=\"✨🧩\";\r\nb c"))
	if p != (pos{2, 1}) {
		t.Fatalf("%+v", p)
	}
	p = advance(pos{}, []byte("x=\"✨🧩\";"))
	if p != (pos{0, 8}) { // x = " ✨ 🧩🧩 " ;
		t.Fatalf("%+v", p)
	}
}

// A slash after a value divides; after an operator or a keyword it starts a regular expression,
// whose braces and quotes don't count.
func TestRegularExpressionsAreNotCode(t *testing.T) {
	src := `var a=b/2/c;function f(){return/[}"']/.test(a)}var g=x=>x/2;`
	fns, err := ReadFunctions([]byte(src), map[pos]string{})
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := innermost(fns, 0, 30); name != "f" {
		t.Fatalf("in f: %q (%v)", name, fns)
	}
	if name, _ := innermost(fns, 0, 56); name != "g" {
		t.Fatalf("in g: %q (%v)", name, fns)
	}
	if _, in := innermost(fns, 0, 3); in {
		t.Fatal("var a is in no function")
	}
}
