// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"bufio"
	"bytes"
	"os"
	"strings"
)

// Three-way merge of text files for `ore sync --inline`: ours and theirs
// against their common base, line by line. Regions only one side changed
// take that side; regions both sides changed identically take it once;
// regions both sides changed differently become a conflict block with
// markers. The algorithm is the classic diff3 over LCS matchings.

// Merge3 merges ours and theirs against base. It returns the result and the
// number of conflict blocks; binary content (a NUL byte on any side) is not
// merged and reports -1.
func Merge3(base, ours, theirs []byte, oursLabel, theirsLabel string) ([]byte, int) {
	if bytes.IndexByte(base, 0) >= 0 || bytes.IndexByte(ours, 0) >= 0 || bytes.IndexByte(theirs, 0) >= 0 {
		return nil, -1
	}
	b, a, c := splitLines(base), splitLines(ours), splitLines(theirs)
	ma := matches(b, a)
	mc := matches(b, c)
	var out []string
	conflicts := 0
	ia, ic := 0, 0 // next unconsumed line of ours and theirs
	i := 0         // next line of base
	for i <= len(b) {
		// stable lines: matched on both sides as the very next line
		if i < len(b) {
			if ja, ok := ma[i]; ok && ja == ia {
				if jc, ok := mc[i]; ok && jc == ic {
					out = append(out, b[i])
					i++
					ia++
					ic++
					continue
				}
			}
		}
		// a change region: up to the next line stable on both sides
		j := i + 1
		for j < len(b) {
			ja, oka := ma[j]
			jc, okc := mc[j]
			if oka && okc && ja >= ia && jc >= ic {
				break
			}
			j++
		}
		endA, endC := len(a), len(c)
		if j < len(b) {
			endA, endC = ma[j], mc[j]
		}
		if i >= len(b) && endA == ia && endC == ic {
			break // nothing left on any side
		}
		baseR, oursR, theirsR := b[i:min(j, len(b))], a[ia:endA], c[ic:endC]
		switch {
		case equal(oursR, baseR):
			out = append(out, theirsR...)
		case equal(theirsR, baseR), equal(oursR, theirsR):
			out = append(out, oursR...)
		default:
			conflicts++
			out = append(out, "<<<<<<< "+oursLabel+"\n")
			out = append(out, withNewlines(oursR)...)
			out = append(out, "=======\n")
			out = append(out, withNewlines(theirsR)...)
			out = append(out, ">>>>>>> "+theirsLabel+"\n")
		}
		i = j
		ia, ic = endA, endC
	}
	return []byte(strings.Join(out, "")), conflicts
}

// withNewlines terminates a region's last line so the marker after it
// starts on its own line.
func withNewlines(lines []string) []string {
	if len(lines) == 0 || strings.HasSuffix(lines[len(lines)-1], "\n") {
		return lines
	}
	out := append([]string{}, lines...)
	out[len(out)-1] += "\n"
	return out
}

func equal(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// splitLines splits content into lines that keep their newline; a final
// line without one is kept as is, so "a\n" and "a" differ.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// matches returns the LCS matching of a onto b as a map from an index in a
// to its matched index in b (monotonic), by Myers' O(ND) algorithm.
func matches(a, b []string) map[int]int {
	n, m := len(a), len(b)
	out := map[int]int{}
	if n == 0 || m == 0 {
		return out
	}
	max := n + m
	v := make([]int, 2*max+2)
	var trace [][]int
	offset := max
	found := false
	for d := 0; d <= max && !found; d++ {
		if d*len(v) > 64<<20 { // a wholesale rewrite: no useful alignment, one conflict block
			return map[int]int{}
		}
		vc := make([]int, len(v))
		copy(vc, v)
		trace = append(trace, vc)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	// backtrack through the trace to recover the matched (diagonal) lines
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		vd := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vd[offset+k-1] < vd[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vd[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			out[x] = y
		}
		if d > 0 {
			x, y = prevX, prevY
		}
	}
	return out
}

// hasMarkers reports whether a file still carries conflict markers.
func hasMarkers(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "<<<<<<< ") || strings.HasPrefix(l, ">>>>>>> ") {
			return true
		}
	}
	return false
}
