package align

import (
	"math"
	"math/rand"
	"testing"

	"github.com/hranicka/mediatool/internal/vhash"
)

func TestBuildTimingMapConstantOffset(t *testing.T) {
	// dub frame j matches reference frame j+10 at interval 0.2: a constant
	// +2 s offset over the whole 10 s dub
	var matches []frameMatch
	for j := 0; j < 50; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j + 10, dist: 0})
	}
	tm, err := buildTimingMap(matches, 0.2, 12, 10, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if tm.NRel != 50 || tm.NOut != 0 {
		t.Errorf("counts: nRel=%d nOut=%d, want 50/0", tm.NRel, tm.NOut)
	}
	if len(tm.Segments) != 1 {
		t.Fatalf("segments: got %d, want 1", len(tm.Segments))
	}
	s := tm.Segments[0]
	if math.Abs(s.RefStart-2) > 1e-6 {
		t.Errorf("ref start: got %v, want 2", s.RefStart)
	}
	if math.Abs(s.RefEnd-12) > 1e-6 {
		t.Errorf("ref end: got %v, want 12", s.RefEnd)
	}
	if math.Abs(s.DubEnd-10) > 1e-6 {
		t.Errorf("dub end: got %v, want 10", s.DubEnd)
	}
	if s.Slope < 0.999 || s.Slope > 1.001 {
		t.Errorf("slope: got %v, want 1", s.Slope)
	}
	if math.Abs(tm.RefCoverage-10.0/12) > 1e-6 {
		t.Errorf("ref coverage: got %v", tm.RefCoverage)
	}
	if math.Abs(tm.DubCoverage-1) > 1e-6 {
		t.Errorf("dub coverage: got %v", tm.DubCoverage)
	}
}

func TestBuildTimingMapSplitsOnJump(t *testing.T) {
	// 10 s of continuous content, then a 10 s reference-only gap (the dub
	// skipped a section), then 10 s more
	var matches []frameMatch
	for j := 0; j < 50; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j, dist: 0})
	}
	for j := 50; j < 100; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j + 50, dist: 0})
	}
	tm, err := buildTimingMap(matches, 0.2, 30, 20, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if len(tm.Segments) != 2 {
		t.Fatalf("segments: got %d, want 2 (%+v)", len(tm.Segments), tm.Segments)
	}
	if tm.Segments[0].RefEnd > 10.1 {
		t.Errorf("first segment must end near 10 s, got %v", tm.Segments[0].RefEnd)
	}
	if tm.Segments[1].RefStart < 19.9 {
		t.Errorf("second segment must start near 20 s, got %v", tm.Segments[1].RefStart)
	}
	// the 10 s reference gap between the segments has no dub counterpart
	covered := tm.Segments[0].RefEnd - tm.Segments[0].RefStart +
		tm.Segments[1].RefEnd - tm.Segments[1].RefStart
	if math.Abs(covered-20) > 0.5 {
		t.Errorf("covered reference span: got %.1f, want ~20", covered)
	}
}

func TestBuildTimingMapFramesRateSlope(t *testing.T) {
	// a PAL-speedup dub: 4.3 % shorter than the reference
	var matches []frameMatch
	for j := 0; j < 200; j++ {
		matches = append(matches, frameMatch{dub: j, ref: int(math.Round(float64(j) * 1.043)), dist: 0})
	}
	tm, err := buildTimingMap(matches, 0.2, 42, 40, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if len(tm.Segments) != 1 {
		t.Fatalf("segments: got %d, want 1", len(tm.Segments))
	}
	if s := tm.Segments[0].Slope; s < 1.02 || s > 1.07 {
		t.Errorf("slope: got %v, want ~1.043", s)
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
	tm, err := buildTimingMap(matches, 0.2, 12, 10, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if tm.NRel != 49 {
		t.Errorf("nRel: got %d, want 49 (one match beyond the distance)", tm.NRel)
	}
	if tm.NOut != 1 {
		t.Errorf("nOut: got %d, want 1 (the 20 s outlier)", tm.NOut)
	}
	if len(tm.Segments) != 1 {
		t.Fatalf("segments: got %d, want 1", len(tm.Segments))
	}
	if math.Abs(tm.Segments[0].RefStart-1) > 0.2 {
		t.Errorf("ref start: got %v, want ~1", tm.Segments[0].RefStart)
	}
}

func TestBuildTimingMapTooFewMatches(t *testing.T) {
	var matches []frameMatch
	for j := 0; j < 9; j++ {
		matches = append(matches, frameMatch{dub: j, ref: j + 5, dist: 0})
	}
	if _, err := buildTimingMap(matches, 0.2, 10, 10, 45); err == nil {
		t.Fatal("expected an error below 10 reliable matches")
	}
}

func TestCoverageGuard(t *testing.T) {
	tm := &timingMap{RefCoverage: 0.4, DubCoverage: 0.9}
	if err := tm.checkCoverage(); err == nil {
		t.Fatal("expected an error when only 40% of the reference is covered")
	}
	tm = &timingMap{RefCoverage: 0.95, DubCoverage: 0.95}
	if err := tm.checkCoverage(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFindMatchesShiftedCopy(t *testing.T) {
	ref := hashesOf(0, 40)
	dub := hashesOf(3, 25) // the same content 3 frames in
	matches := findMatches(ref, dub, 5, 45, 0.2)
	if len(matches) != 25 {
		t.Fatalf("got %d matches, want 25", len(matches))
	}
	for _, m := range matches {
		if m.ref != m.dub+3 || m.dist != 0 {
			t.Fatalf("match %v, want dub+3 dist 0", m)
		}
	}
}

func TestFindMatchesResyncsLateStart(t *testing.T) {
	ref := hashesOf(0, 100)
	dub := hashesOf(40, 40) // the dub starts 8 s into the reference
	matches := findMatches(ref, dub, 5, 45, 0.2)
	if len(matches) == 0 || matches[0].ref != 40 {
		t.Fatalf("first match: got %+v, want ref 40", matches[:min(1, len(matches))])
	}
	for _, m := range matches {
		if m.ref != m.dub+40 {
			t.Fatalf("match %v, want dub+40 (late start)", m)
		}
	}
}

func TestFindMatchesResyncsOverExtraScene(t *testing.T) {
	ref := hashesOf(0, 100)
	rng := rand.New(rand.NewSource(7))
	dub := append(append(hashesOf(0, 30), randomHashes(rng, 20)...), hashesOf(30, 50)...)
	matches := findMatches(ref, dub, 5, 45, 0.2)
	// the frames after the inserted scene must reattach 20 frames earlier
	for _, m := range matches[50:] {
		if m.ref != -1 && m.ref != m.dub-20 {
			t.Fatalf("match %v, want dub-20 after the extra scene", m)
		}
	}
	found := 0
	for _, m := range matches[50:] {
		if m.ref == m.dub-20 {
			found++
		}
	}
	if found < 30 {
		t.Errorf("re-synced matches after the extra scene: got %d, want >= 30", found)
	}
}

func TestFindMatchesFailsOnNoise(t *testing.T) {
	ref := hashesOf(0, 40)
	rng := rand.New(rand.NewSource(9))
	dub := randomHashes(rng, 25)
	matches := findMatches(ref, dub, 5, 45, 0.2)
	reliable := 0
	for _, m := range matches {
		if m.ref >= 0 {
			reliable++
		}
	}
	if reliable > 2 {
		t.Errorf("random noise must not reliably match, got %d close matches", reliable)
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
