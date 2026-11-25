package symbols

import "sort"

// Frame is one function in a symbolicated frame: what the offset is in, innermost first, then
// each function it was inlined into.
type Frame struct {
	Function string `json:"function"`
	File     string `json:"file,omitempty"`
	Line     uint64 `json:"line,omitempty"`
	Inlined  bool   `json:"inlined,omitempty"` // inlined into the next frame
}

// Lookup is what the code at offset is: nil when no function covers it. The server's lookup
// (internal/symbols in Lyrelog) must answer the same: both test against testdata.
func (m *Map) Lookup(offset uint64) []Frame {
	f := sort.Search(len(m.Functions), func(i int) bool { return m.Functions[i][0] > offset }) - 1
	if f < 0 || offset >= m.Functions[f][1] {
		return nil
	}
	fn := m.Functions[f]
	// The inlined calls covering offset, one per depth.
	byDepth := map[uint64][6]uint64{}
	deepest := uint64(0)
	for i := sort.Search(len(m.Inlines), func(i int) bool { return m.Inlines[i][0] >= fn[0] }); i < len(m.Inlines) && m.Inlines[i][0] <= offset; i++ {
		in := m.Inlines[i]
		if offset < in[1] && in[1] <= fn[1] {
			byDepth[in[2]] = in
			deepest = max(deepest, in[2])
		}
	}
	file, line := uint64(0), uint64(0)
	if l := sort.Search(len(m.Lines), func(i int) bool { return m.Lines[i][0] > offset }) - 1; l >= 0 {
		file, line = m.Lines[l][1], m.Lines[l][2]
	}
	var out []Frame
	for d := deepest; d >= 1; d-- {
		in, ok := byDepth[d]
		if !ok {
			break // a gap in the chain: stop at what's known for sure
		}
		out = append(out, Frame{Function: m.Strings[in[3]], File: m.Strings[file], Line: line, Inlined: true})
		file, line = in[4], in[5]
	}
	return append(out, Frame{Function: m.Strings[fn[2]], File: m.Strings[file], Line: line})
}
