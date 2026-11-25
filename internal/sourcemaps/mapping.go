package sourcemaps

import (
	"errors"
	"strings"
)

// The few things the CLI reads in a source map's mappings: which identifiers of the bundle the
// map names (a minifier renames `Board` to `n` and the map says so), and how to move what's
// after a point of a line when the debug id's snippet is put there.

const base64Digits = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var base64Value = func() (t [256]int8) {
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(base64Digits); i++ {
		t[base64Digits[i]] = int8(i)
	}
	return t
}()

var errMappings = errors.New("the source map's mappings can't be read")

func readVLQ(s string) (int64, string, bool) {
	var v int64
	shift := 0
	for i := 0; i < len(s); i++ {
		d := base64Value[s[i]]
		if d < 0 || shift > 32 {
			return 0, "", false
		}
		v |= int64(d&31) << shift
		shift += 5
		if d&32 == 0 {
			if v&1 != 0 {
				return -(v >> 1), s[i+1:], true
			}
			return v >> 1, s[i+1:], true
		}
	}
	return 0, "", false
}

func writeVLQ(b *strings.Builder, v int64) {
	u := v << 1
	if v < 0 {
		u = (-v << 1) | 1
	}
	for {
		d := u & 31
		u >>= 5
		if u > 0 {
			d |= 32
		}
		b.WriteByte(base64Digits[d])
		if u == 0 {
			return
		}
	}
}

// pos is a generated position: 0-based line, column in UTF-16 code units.
type pos struct{ line, col uint32 }

// namedAt are the map's names by the generated position they're at: what a minified identifier
// was called.
func namedAt(mappings string, names []string, lineOff, colOff uint32, into map[pos]string) error {
	var name int64 // the only field read: the others are relative too, but not needed
	line := lineOff
	for _, text := range strings.Split(mappings, ";") {
		var col int64
		if line == lineOff {
			col = int64(colOff)
		}
		for _, seg := range strings.Split(text, ",") {
			if seg == "" {
				continue
			}
			var f [5]int64
			n := 0
			for rest := seg; rest != ""; n++ {
				if n == 5 {
					return errMappings
				}
				v, r, ok := readVLQ(rest)
				if !ok {
					return errMappings
				}
				f[n], rest = v, r
			}
			col += f[0]
			if n == 5 {
				name += f[4]
				if name >= 0 && name < int64(len(names)) && col >= 0 {
					into[pos{line, uint32(col)}] = names[name]
				}
			}
		}
		line++
	}
	return nil
}

// shiftLine moves the segments of a generated line at or after col by n columns: what putting n
// UTF-16 units of code there does to the map.
func shiftLine(mappings string, line, col uint32, n int64) (string, error) {
	start := 0
	for i := uint32(0); i < line; i++ {
		j := strings.IndexByte(mappings[start:], ';')
		if j < 0 {
			return mappings, nil // the map has nothing on that line
		}
		start += j + 1
	}
	end := strings.IndexByte(mappings[start:], ';')
	if end < 0 {
		end = len(mappings)
	} else {
		end += start
	}
	text := mappings[start:end]
	var abs int64
	offset := 0
	for _, seg := range strings.Split(text, ",") {
		if seg == "" {
			offset++
			continue
		}
		v, rest, ok := readVLQ(seg)
		if !ok {
			return "", errMappings
		}
		abs += v
		if abs >= int64(col) {
			// The first segment at or after the point: its column is relative to the one before,
			// so moving it moves every one after it on the line.
			var b strings.Builder
			b.WriteString(mappings[:start+offset])
			writeVLQ(&b, v+n)
			b.WriteString(rest)
			b.WriteString(mappings[start+offset+len(seg):])
			return b.String(), nil
		}
		offset += len(seg) + 1
	}
	return mappings, nil
}
