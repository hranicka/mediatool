package dedup

import (
	"math"

	"github.com/hranicka/mediatool/internal/vhash"
)

// durationCompatible reports whether two durations are close enough for a
// candidate pair: a fixed 10 s floor covers small header/trailer or
// rounding differences, tolPct covers framerate variants and cut scenes.
func durationCompatible(d1, d2, tolPct float64) bool {
	maxDiff := math.Max(10, math.Max(d1, d2)*tolPct/100)
	return math.Abs(d1-d2) <= maxDiff
}

// matchFraction walks the shorter signature list (the driver) and matches
// each driver frame against the best frame within a ±window neighborhood
// of the previous match position, so shifted starts and progressive
// framerate drift stay covered (the align_audio.py find_matches scheme).
// Returns the fraction of driver frames with a reliable match (hamming
// distance <= maxBits); bails out early when min-match is unreachable.
// The bail keeps the >= min-match decision exact, but the returned value
// itself can sit lower than a full walk would yield (the walk may recover
// later frames) - only compare it against minMatch.
func matchFraction(a, b [][vhash.HashLen]byte, window, maxBits int, minMatch float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(b) < len(a) {
		a, b = b, a
	}
	need := int(math.Ceil(minMatch * float64(len(a))))
	prev := 0
	matched := 0
	for j := range a {
		if matched+(len(a)-j) < need {
			break // even matching every remaining frame cannot reach the threshold
		}
		lo := prev - window
		if lo < 0 {
			lo = 0
		}
		hi := prev + window + 1
		if hi > len(b) {
			hi = len(b)
		}
		best, bestDist := vhash.WindowBest(a, b, j, lo, hi, prev+1)
		if bestDist <= maxBits {
			matched++
		}
		// the walk continues from the best position even when unreliable,
		// keeping the monotonic track through outlier frames
		prev = best
	}
	return float64(matched) / float64(len(a))
}

// findRoot resolves the root of x's set with path halving.
func findRoot(parent []int, x int) int {
	for parent[x] != x {
		parent[x] = parent[parent[x]]
		x = parent[x]
	}
	return x
}

// unionGroups merges matched pairs into connected components and returns
// those with at least two members, each sorted by member index.
func unionGroups(n int, pairs [][2]int) [][]int {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	for _, p := range pairs {
		rx, ry := findRoot(parent, p[0]), findRoot(parent, p[1])
		if rx != ry {
			parent[rx] = ry
		}
	}
	comp := map[int][]int{}
	for i := 0; i < n; i++ {
		root := findRoot(parent, i)
		comp[root] = append(comp[root], i)
	}
	var out [][]int
	for _, members := range comp {
		if len(members) >= 2 {
			out = append(out, members)
		}
	}
	return out
}
