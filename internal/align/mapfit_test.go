package align

import (
	"math"
	"math/rand"
	"testing"

	"github.com/hranicka/mediatool/internal/vhash"
)

func TestPolyfit2(t *testing.T) {
	// y = 1 + 2x + 0.5x²
	xs := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	ys := make([]float64, len(xs))
	for i, x := range xs {
		ys[i] = 1 + 2*x + 0.5*x*x
	}
	c := polyfit2(xs, ys)
	for i, want := range [3]float64{1, 2, 0.5} {
		if math.Abs(c[i]-want) > 1e-9 {
			t.Errorf("coef[%d]: got %v, want %v", i, c[i], want)
		}
	}
}

func TestBuildTimingMapConstantOffset(t *testing.T) {
	// dub frame j matches reference frame j+10 at interval 0.2: a constant
	// +2 s offset
	var matches []frameMatch
	for j := 0; j < 50; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j + 10, dist: 0})
	}
	cz, en, nRel, nOut, err := buildTimingMap(matches, 0.2, 10, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if nRel != 50 || nOut != 0 {
		t.Errorf("counts: nRel=%d nOut=%d, want 50/0", nRel, nOut)
	}
	if len(cz) != 11 || len(en) != 11 {
		t.Fatalf("grid length: got %d, want 11 (dub 10 s + 1)", len(cz))
	}
	if math.Abs(en[0]-2) > 1e-6 {
		t.Errorf("en[0]: got %v, want 2", en[0])
	}
	if math.Abs(en[10]-12) > 1e-6 {
		t.Errorf("en[10]: got %v, want 12", en[10])
	}
}

func TestBuildTimingMapDropsOutliers(t *testing.T) {
	// constant +1 s offset, one bogus match 20 s off the trend (still
	// "reliable" by distance) and two beyond the distance threshold
	var matches []frameMatch
	for j := 0; j < 50; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j + 5, dist: 1})
	}
	matches[20] = frameMatch{dub: 20, ref: 20 + 5 + 100, dist: 1} // +21 s offset outlier
	matches[30] = frameMatch{dub: 30, ref: 30 + 5, dist: 100}     // unreliable, excluded up front
	_, en, nRel, nOut, err := buildTimingMap(matches, 0.2, 10, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if nRel != 49 {
		t.Errorf("nRel: got %d, want 49 (one match beyond the distance)", nRel)
	}
	if nOut != 1 {
		t.Errorf("nOut: got %d, want 1 (the 20 s outlier)", nOut)
	}
	if math.Abs(en[0]-1) > 1e-6 {
		t.Errorf("en[0]: got %v, want 1", en[0])
	}
}

func TestBuildTimingMapTooFewMatches(t *testing.T) {
	var matches []frameMatch
	for j := 0; j < 9; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j + 5, dist: 0})
	}
	if _, _, _, _, err := buildTimingMap(matches, 0.2, 10, 45); err == nil {
		t.Fatal("expected an error below 10 reliable matches")
	}
}

func TestFindMatchesShiftedCopy(t *testing.T) {
	ref := hashesOf(0, 40)
	dub := hashesOf(3, 25) // the same content 3 frames in
	matches := findMatches(ref, dub, 5)
	if len(matches) != 25 {
		t.Fatalf("got %d matches, want 25", len(matches))
	}
	for _, m := range matches {
		if m.ref != m.dub+3 || m.dist != 0 {
			t.Fatalf("match %v, want dub+3 dist 0", m)
		}
	}
}

func TestFindMatchesFailsOnNoise(t *testing.T) {
	ref := hashesOf(0, 40)
	rng := rand.New(rand.NewSource(9))
	dub := randomHashes(rng, 25)
	matches := findMatches(ref, dub, 5)
	reliable := 0
	for _, m := range matches {
		if m.dist < 100 {
			reliable++
		}
	}
	if reliable > 2 {
		t.Errorf("random noise must not reliably match, got %d close matches", reliable)
	}
}

func TestInterp1(t *testing.T) {
	xs := []float64{0, 1, 2, 3}
	ys := []float64{10, 20, 30, 40}
	tests := map[float64]float64{
		-1:  10, // clamp below
		0:   10,
		0.5: 15,
		2:   30,
		2.5: 35,
		4:   40, // clamp above
	}
	for in, want := range tests {
		if got := interp1(xs, ys, in); got != want {
			t.Errorf("interp1(%v): got %v, want %v", in, got, want)
		}
	}
}

// hashesOf builds signature lists of pseudo-random per-frame content: the
// frame index seeds the pixels, so a shifted list is shifted content.
func hashesOf(start, count int) [][vhash.HashLen]byte {
	out := make([][vhash.HashLen]byte, 0, count)
	for t := start; t < start+count; t++ {
		rng := rand.New(rand.NewSource(int64(t)))
		var h [vhash.HashLen]byte
		rng.Read(h[:])
		out = append(out, h)
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
