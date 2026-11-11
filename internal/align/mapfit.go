package align

import (
	"fmt"
	"math"
	"sort"

	"github.com/hranicka/mediatool/internal/vhash"
)

// frameMatch is one dubbed frame's best reference match: the dubbed frame
// index, the reference frame index and the hash distance.
type frameMatch struct {
	dub, ref, dist int
}

// findMatches pairs each dubbed frame with its best-matching reference
// frame within a ±window neighborhood of the previous match (the mapping
// is smooth and monotonic, so the window keeps the search fast and on
// track). Port of align_audio.py find_matches.
func findMatches(ref, dub [][vhash.HashLen]byte, window int) []frameMatch {
	matches := make([]frameMatch, 0, len(dub))
	prev := 0
	for j := range dub {
		lo := max(0, prev-window)
		hi := min(len(ref), prev+window+1)
		best, bestDist := lo, -1
		for k := lo; k < hi; k++ {
			if d := vhash.Hamming(dub[j], ref[k]); bestDist < 0 || d < bestDist {
				best, bestDist = k, d
			}
		}
		matches = append(matches, frameMatch{dub: j, ref: best, dist: bestDist})
		prev = best
	}
	return matches
}

// buildTimingMap fits a smooth monotonic map dubbedTime -> referenceTime
// from the reliable matches: a quadratic offset fit with one round of
// outlier rejection, evaluated on a 1 s grid over the dub. Port of
// align_audio.py build_timing_map.
func buildTimingMap(matches []frameMatch, interval, dubDur float64, matchDist int) (czGrid, enGrid []float64, nRel, nOut int, err error) {
	var cts, offs []float64
	for _, m := range matches {
		if m.dist < matchDist {
			cts = append(cts, float64(m.dub)*interval)
			offs = append(offs, float64(m.ref)*interval-float64(m.dub)*interval)
		}
	}
	nRel = len(cts)
	if nRel < 10 {
		return nil, nil, nRel, 0, fmt.Errorf("too few reliable matches (%d) to build a timing map", nRel)
	}

	coef := polyfit2(cts, offs)
	keep := make([]bool, nRel)
	nKept := 0
	for i := range offs {
		keep[i] = math.Abs(offs[i]-polyval2(coef, cts[i])) < 8
		if keep[i] {
			nKept++
		}
	}
	coef2 := coef
	if nKept >= 3 {
		kc := make([]float64, 0, nKept)
		ko := make([]float64, 0, nKept)
		for i := range offs {
			if keep[i] {
				kc = append(kc, cts[i])
				ko = append(ko, offs[i])
			}
		}
		coef2 = polyfit2(kc, ko)
	}
	nOut = nRel - nKept

	// 1 s grid over the dub, reference times enforced monotonic
	n := int(dubDur) + 1
	czGrid = make([]float64, n)
	enGrid = make([]float64, n)
	for i := range czGrid {
		czGrid[i] = float64(i)
		enGrid[i] = czGrid[i] + polyval2(coef2, czGrid[i])
	}
	for i := 1; i < n; i++ {
		if enGrid[i] <= enGrid[i-1] {
			enGrid[i] = enGrid[i-1] + 0.05
		}
	}
	return czGrid, enGrid, nRel, nOut, nil
}

// polyfit2 fits y = c0 + c1*x + c2*x² by least squares (3x3 normal
// equations solved by Gaussian elimination).
func polyfit2(xs, ys []float64) [3]float64 {
	var m [3][3]float64
	var v [3]float64
	for i := range xs {
		p := [3]float64{1, xs[i], xs[i] * xs[i]}
		for r := 0; r < 3; r++ {
			v[r] += p[r] * ys[i]
			for c := 0; c < 3; c++ {
				m[r][c] += p[r] * p[c]
			}
		}
	}
	return solve3(m, v)
}

func polyval2(c [3]float64, x float64) float64 {
	return c[0] + c[1]*x + c[2]*x*x
}

// solve3 solves a 3x3 linear system by Gauss-Jordan elimination with
// partial pivoting; the normal equations of a least-squares fit with
// enough well-spread points are never singular, so no error plumbing.
func solve3(m [3][3]float64, v [3]float64) [3]float64 {
	a := m
	b := v
	for col := 0; col < 3; col++ {
		piv := col
		for r := col + 1; r < 3; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		a[col], a[piv] = a[piv], a[col]
		b[col], b[piv] = b[piv], b[col]
		for r := 0; r < 3; r++ {
			if r == col {
				continue
			}
			f := a[r][col] / a[col][col]
			for c := col; c < 3; c++ {
				a[r][c] -= f * a[col][c]
			}
			b[r] -= f * b[col]
		}
	}
	return [3]float64{b[0] / a[0][0], b[1] / a[1][1], b[2] / a[2][2]}
}

// interp1 evaluates the piecewise-linear interpolation of ys over xs
// (strictly increasing); values outside the grid clamp to the end values.
func interp1(xs, ys []float64, x float64) float64 {
	if x <= xs[0] {
		return ys[0]
	}
	if x >= xs[len(xs)-1] {
		return ys[len(ys)-1]
	}
	i := sort.SearchFloat64s(xs, x)
	if i >= len(xs) {
		return ys[len(ys)-1]
	}
	if xs[i] == x {
		return ys[i]
	}
	f := (x - xs[i-1]) / (xs[i] - xs[i-1])
	return ys[i-1]*(1-f) + ys[i]*f
}
