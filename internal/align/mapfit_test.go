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

func TestBuildTimingMapFineResolution(t *testing.T) {
	// a 1.13 s offset with 0.05 s sampling: matches land on the frame grid,
	// so the result must stay within half an interval of the true offset
	const interval = 0.05
	var matches []frameMatch
	for j := 0; j < 1200; j++ { // 60 s
		ref := int(math.Round((float64(j)*interval + 1.13) / interval))
		matches = append(matches, frameMatch{dub: j, ref: ref, dist: 1})
	}
	tm, err := buildTimingMap(matches, interval, 62, 60, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if len(tm.Segments) != 1 {
		t.Fatalf("segments: got %d, want 1", len(tm.Segments))
	}
	got := tm.Segments[0].refAt(0)
	if math.Abs(got-1.13) > interval/2 {
		t.Errorf("offset: got %.3f, want 1.13 ± %.3f", got, interval/2)
	}
	if tm.Segments[0].Residual > interval/2 {
		t.Errorf("fit residual: got %.3f, want <= %.3f", tm.Segments[0].Residual, interval/2)
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
	tm = &timingMap{RefCoverage: 0.79, DubCoverage: 0.95}
	if err := tm.checkCoverage(); err == nil {
		t.Fatal("expected an error when only 79% of the reference is covered")
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

func TestFindMatchesRejectsBackwardRepeat(t *testing.T) {
	// the dub repeats a 10 s scene: matching it backwards would rewind the
	// map. The walk must stay lost through the repeat and continue at the
	// reference position after it.
	ref := hashesOf(0, 300)
	dub := append(append(hashesOf(0, 100), hashesOf(50, 50)...), hashesOf(100, 100)...)
	matches := findMatches(ref, dub, 50, 45, 0.2)
	prev := -1
	for _, m := range matches {
		if m.ref < 0 {
			continue
		}
		if m.ref < prev-2 {
			t.Fatalf("reference position rewound more than jitter: %d after %d", m.ref, prev)
		}
		prev = m.ref
	}
	for j := 150; j < 250; j++ {
		if matches[j].ref != j-50 {
			t.Fatalf("after the repeat, frame %d matched ref %d, want %d", j, matches[j].ref, j-50)
		}
	}
}

func TestResyncMatchExactPosition(t *testing.T) {
	// Perceptual hashes are unique per frame, so the resync scan must find
	// the exact position at every grid phase, not just where a coarse stride
	// happens to land.
	ref := hashesOf(0, 1200)
	for off := 0; off < 200; off++ {
		dub := hashesOf(off, 60)
		r, d, ok := resyncMatch(ref, dub, 0, 0, 45, 0.05)
		if !ok || r != off || d != 0 {
			t.Fatalf("offset %d: got ref %d dist %d ok %v", off, r, d, ok)
		}
	}
}

func TestFindMatchesResyncsBeyondWindow(t *testing.T) {
	// A jump larger than the local walk must reattach at the first lost
	// frame, not seconds later.
	const interval = 0.05
	ref := hashesOf(0, 4000)
	const skip = 300 // 15 s at 0.05
	dub := append(hashesOf(0, 200), hashesOf(200+skip, 300)...)
	matches := findMatches(ref, dub, 200, 45, interval)
	for j := 200; j < len(dub); j++ {
		if matches[j].ref != j+skip {
			t.Fatalf("frame %d after the jump: got ref %d, want %d", j, matches[j].ref, j+skip)
		}
	}
}

func TestBuildTimingMapMissingScene(t *testing.T) {
	// the dub skips reference 50-65 s: exactly that span stays silent, with
	// no extra silence from a late reattachment
	const interval = 0.05
	ref := hashesOf(0, 3000)
	dub := append(hashesOf(0, 1000), hashesOf(1300, 1000)...)
	matches := findMatches(ref, dub, 200, 45, interval)
	tm, err := buildTimingMap(matches, interval, 150, 100, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if len(tm.Segments) != 2 {
		t.Fatalf("segments: got %d, want 2 (%+v)", len(tm.Segments), tm.Segments)
	}
	if got := tm.Segments[0].RefEnd; math.Abs(got-50) > 0.2 {
		t.Errorf("first segment end: got %.2f, want 50", got)
	}
	if got := tm.Segments[1].RefStart; math.Abs(got-65) > 0.2 {
		t.Errorf("second segment start: got %.2f, want 65", got)
	}
	if gap := tm.Segments[1].RefStart - tm.Segments[0].RefEnd; math.Abs(gap-15) > 0.25 {
		t.Errorf("silent span: got %.2f, want 15", gap)
	}
}

func TestBuildTimingMapExtraScene(t *testing.T) {
	// the dub inserts 15 s of unrelated content at 10 s: the insert is
	// dropped and the reference stays continuous (no false silence)
	const interval = 0.05
	ref := hashesOf(0, 2000)
	rng := rand.New(rand.NewSource(11))
	dub := append(append(hashesOf(0, 200), randomHashes(rng, 300)...), hashesOf(200, 600)...)
	matches := findMatches(ref, dub, 200, 45, interval)
	tm, err := buildTimingMap(matches, interval, 100, 55, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if len(tm.Segments) != 2 {
		t.Fatalf("segments: got %d, want 2 (%+v)", len(tm.Segments), tm.Segments)
	}
	if gap := tm.Segments[1].RefStart - tm.Segments[0].RefEnd; math.Abs(gap) > 0.1 {
		t.Errorf("false reference gap after an extra scene: got %.2f", gap)
	}
}

func TestNormalizeJoinsRemovesRewind(t *testing.T) {
	segs := []segment{
		{DubStart: 0, DubEnd: 10, RefStart: 0, RefEnd: 10, Slope: 1, Intercept: 0},
		// the fitted second line would read dub 9.9 at ref 10: a 0.1 s rewind
		{DubStart: 10, DubEnd: 20, RefStart: 10, RefEnd: 20, Slope: 1, Intercept: 0.1},
	}
	got := normalizeJoins(segs, 30)
	if len(got) != 2 {
		t.Fatalf("segments: got %d, want 2", len(got))
	}
	if d := got[1].dubAt(got[1].RefStart); d < got[0].DubEnd-1e-9 {
		t.Errorf("join still rewinds: next reads %.3f before prev ends at %.3f", d, got[0].DubEnd)
	}
	if got[1].RefEnd-got[1].RefStart < 9.99 {
		t.Errorf("normalization must keep the reference span, got %.2f", got[1].RefEnd-got[1].RefStart)
	}
}

func TestNonOverlappingTrimsBoundaryOverlap(t *testing.T) {
	segs := []segment{
		{DubStart: 0, DubEnd: 10, RefStart: 0, RefEnd: 10, Slope: 1},
		// a legitimate later segment whose noisy start overlaps by 0.7 s
		{DubStart: 14.4, DubEnd: 35, RefStart: 9.3, RefEnd: 29.9, Slope: 1, Intercept: -5.1},
	}
	got := nonOverlapping(segs)
	if len(got) != 2 {
		t.Fatalf("segments: got %d, want 2 (a boundary overlap must be trimmed, not dropped)", len(got))
	}
	if got[1].RefStart < got[0].RefEnd-1e-9 {
		t.Errorf("overlap not resolved: next ref start %.3f before prev ref end %.3f", got[1].RefStart, got[0].RefEnd)
	}
	if got[1].RefEnd-got[1].RefStart < 19 {
		t.Errorf("trimming must keep the bulk of the segment, got %.2f s", got[1].RefEnd-got[1].RefStart)
	}
	// a segment that mostly conflicts with the previous one is dropped
	got = nonOverlapping([]segment{
		{DubStart: 0, DubEnd: 10, RefStart: 0, RefEnd: 10, Slope: 1},
		{DubStart: 10, DubEnd: 11, RefStart: 5, RefEnd: 6, Slope: 1},
	})
	if len(got) != 1 {
		t.Fatalf("segments: got %d, want 1 (a mostly overlapping segment is noise)", len(got))
	}
}

func TestBridgeGaps(t *testing.T) {
	// a 0.3 s match loss: offsets agree, so the span must be bridged
	segs := []segment{
		{DubStart: 0, DubEnd: 10, RefStart: 0, RefEnd: 10, Slope: 1},
		{DubStart: 10.3, DubEnd: 20, RefStart: 10.3, RefEnd: 20, Slope: 1},
	}
	got := bridgeGaps(segs, 20, 20)
	if gap := got[1].RefStart - got[0].RefEnd; gap > 1e-9 {
		t.Errorf("match-loss gap must be bridged, still %.3f s", gap)
	}
	// a real removed scene shifts the offset: keep the silence
	segs = []segment{
		{DubStart: 0, DubEnd: 10, RefStart: 0, RefEnd: 10, Slope: 1},
		{DubStart: 10, DubEnd: 20, RefStart: 15, RefEnd: 25, Slope: 1},
	}
	got = bridgeGaps(segs, 30, 20)
	if gap := got[1].RefStart - got[0].RefEnd; math.Abs(gap-5) > 1e-9 {
		t.Errorf("removed scene must stay silent, gap %.3f", gap)
	}
}

func TestBuildTimingMapKeepsShortShot(t *testing.T) {
	// a 0.5 s shot between two edits is real audio, not noise: it must not
	// be dropped just because it is shorter than a second
	const interval = 0.05
	var matches []frameMatch
	for j := 0; j < 100; j++ { // 5 s, offset 0
		matches = append(matches, frameMatch{dub: j, ref: j, dist: 0})
	}
	for j := 100; j < 110; j++ { // 0.5 s, shift +2 s
		matches = append(matches, frameMatch{dub: j, ref: j + 40, dist: 0})
	}
	for j := 110; j < 160; j++ { // 2.5 s, shift +3 s
		matches = append(matches, frameMatch{dub: j, ref: j + 60, dist: 0})
	}
	tm, err := buildTimingMap(matches, interval, 20, 8, 45)
	if err != nil {
		t.Fatalf("buildTimingMap: %v", err)
	}
	if len(tm.Segments) != 3 {
		t.Fatalf("segments: got %d, want 3 (%+v)", len(tm.Segments), tm.Segments)
	}
}

func TestSpeedEstimateIgnoresOffsetJump(t *testing.T) {
	// 300 s at 1.0427 with a real 5 s reference-only cut at 100 s: the
	// global speed must stay at the true ratio, not absorb the jump.
	const interval = 0.05
	var rel []timedMatch
	for j := 0; j < 6000; j++ {
		dub := float64(j) * interval
		ref := dub * 1.0427
		if dub >= 100 {
			ref += 5
		}
		rel = append(rel, timedMatch{dub: dub, ref: ref})
	}
	got, ok := speedEstimate(rel)
	if !ok {
		t.Fatal("a 300 s file has enough data for a speed estimate")
	}
	if math.Abs(got-1.0427) > 0.002 {
		t.Errorf("speed: got %.5f, want ~1.0427", got)
	}
}

func TestGlobalFallback(t *testing.T) {
	const interval = 0.05
	// 120 s of the same edit at 1.04 speed, split into 12 noisy segments
	var rel []timedMatch
	for j := 0; j < 2400; j++ {
		dub := float64(j) * interval
		rel = append(rel, timedMatch{dub: dub, ref: dub * 1.04})
	}
	var segs []segment
	for i := 0; i < 12; i++ {
		start := float64(i) * 10
		segs = append(segs, segment{DubStart: start, DubEnd: start + 10, RefStart: start * 1.04, RefEnd: (start + 10) * 1.04})
	}
	s, ok := globalFallback(rel, segs, 1.04, interval, 124.8, 120)
	if !ok {
		t.Fatal("a fragmented same-edit map must fall back to the global fit")
	}
	if s.Slope < 1.03 || s.Slope > 1.05 {
		t.Errorf("global slope: got %.4f, want ~1.04", s.Slope)
	}
	// a real edit shifts the second half: the global line no longer fits
	for i := range rel {
		if rel[i].dub >= 60 {
			rel[i].ref += 5
		}
	}
	if _, ok := globalFallback(rel, segs, 1.04, interval, 129.8, 120); ok {
		t.Error("a map with a real removed scene must not be replaced by a global fit")
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
