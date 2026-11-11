package dedup

import (
	"math/rand"
	"testing"

	"github.com/hranicka/mediatool/internal/vhash"
)

// hashesOf hashes raw frames into signature lists.
func hashesOf(raw []byte) [][vhash.HashLen]byte {
	n := len(raw) / vhash.FrameLen
	out := make([][vhash.HashLen]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, vhash.FrameHash(raw[i*vhash.FrameLen:(i+1)*vhash.FrameLen]))
	}
	return out
}

func randomHashes(rng *rand.Rand, n int) [][vhash.HashLen]byte {
	out := make([][vhash.HashLen]byte, n)
	for i := range out {
		rng.Read(out[i][:])
	}
	return out
}

func TestMatchFractionExactCopy(t *testing.T) {
	a := hashesOf(framesAt(0, 20))
	b := hashesOf(framesAt(0, 20))
	if got := matchFraction(a, b, 5, vhash.HashLen*8/5, 0.8); got != 1 {
		t.Errorf("identical content: got %v, want 1", got)
	}
}

func TestMatchFractionShiftedCopy(t *testing.T) {
	// b is the same content shifted by 3 frames: a shifted start must be
	// walked into range by the window
	a := hashesOf(framesAt(0, 20))
	b := hashesOf(framesAt(3, 15))
	if got := matchFraction(a, b, 5, vhash.HashLen*8/5, 0.8); got != 1 {
		t.Errorf("shifted copy: got %v, want 1", got)
	}
}

func TestMatchFractionDrift(t *testing.T) {
	// a framerate variant: b's frame k matches a's frame k + k/4, a
	// progressively drifting offset the window walk must follow
	a := hashesOf(framesAt(0, 40))
	b := make([][vhash.HashLen]byte, 0, 30)
	for k := 0; k < 30; k++ {
		b = append(b, a[k+k/4])
	}
	if got := matchFraction(a, b, 5, vhash.HashLen*8/5, 0.8); got < 0.8 {
		t.Errorf("drifting copy: got %v, want >= 0.8", got)
	}
}

func TestMatchFractionUnrelated(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	a := hashesOf(framesAt(0, 20))
	b := randomHashes(rng, 20)
	if got := matchFraction(a, b, 5, vhash.HashLen*8/5, 0.8); got >= 0.8 {
		t.Errorf("unrelated content: got %v, want < 0.8", got)
	}
}

func TestMatchFractionShiftedBeyondWindow(t *testing.T) {
	// a constant shift larger than the window must NOT match (the walk
	// cannot reach the true offset)
	a := hashesOf(framesAt(0, 20))
	b := hashesOf(framesAt(10, 15))
	if got := matchFraction(a, b, 5, vhash.HashLen*8/5, 0.8); got >= 0.8 {
		t.Errorf("shift beyond the window: got %v, want < 0.8", got)
	}
}

// fullWalk is matchFraction without the early bail-out. The bail returns
// its running count, which a recovering full walk can still exceed - but
// whenever it fires, even matching every remaining frame stays below the
// threshold, so the >= min-match decision must be identical either way.
// That decision equivalence is the property under test.
func fullWalk(a, b [][vhash.HashLen]byte, window, maxBits int, minMatch float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(b) < len(a) {
		a, b = b, a
	}
	prev := 0
	matched := 0
	for j := range a {
		lo := max(0, prev-window)
		hi := min(len(b), prev+window+1)
		best, bestDist := vhash.WindowBest(a, b, j, lo, hi, prev+1)
		if bestDist <= maxBits {
			matched++
		}
		prev = best
	}
	return float64(matched) / float64(len(a))
}

func TestMatchFractionEarlyExitMatchesFullWalk(t *testing.T) {
	for _, seed := range []int64{1, 7, 13, 42, 99} {
		for _, noise := range []int{1, 3, 5} {
			a := hashesOf(framesAt(0, 24))
			b := make([][vhash.HashLen]byte, 24)
			copy(b, a)
			rng := rand.New(rand.NewSource(seed))
			for k := 0; k < noise; k++ {
				rng.Read(b[rng.Intn(24)][:])
			}
			for _, minMatch := range []float64{0.5, 0.8} {
				got := matchFraction(a, b, 5, vhash.HashLen*8/5, minMatch) >= minMatch
				want := fullWalk(a, b, 5, vhash.HashLen*8/5, minMatch) >= minMatch
				if got != want {
					t.Errorf("seed %d noise %d min-match %v: bailed decision %v, full-walk decision %v",
						seed, noise, minMatch, got, want)
				}
			}
		}
	}
}

func TestMatchFractionEmpty(t *testing.T) {
	a := hashesOf(framesAt(0, 5))
	if got := matchFraction(a, nil, 5, 100, 0.8); got != 0 {
		t.Errorf("empty side: got %v, want 0", got)
	}
	if got := matchFraction(nil, a, 5, 100, 0.8); got != 0 {
		t.Errorf("empty driver: got %v, want 0", got)
	}
}

func TestUnionGroups(t *testing.T) {
	groups := unionGroups(6, [][2]int{{0, 1}, {1, 2}, {3, 4}})
	if len(groups) != 2 {
		t.Fatalf("got %d groups (%v), want 2", len(groups), groups)
	}
	sizes := map[int]bool{}
	for _, g := range groups {
		sizes[len(g)] = true
	}
	if !sizes[3] || !sizes[2] {
		t.Errorf("expected one group of 3 and one of 2: %v", groups)
	}
}

func TestUnionGroupsNoPairs(t *testing.T) {
	if groups := unionGroups(3, nil); len(groups) != 0 {
		t.Errorf("no pairs must yield no groups, got %v", groups)
	}
}
