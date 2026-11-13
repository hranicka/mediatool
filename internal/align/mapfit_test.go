package align

import (
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/vhash"
)

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

// speedDub builds a dub that samples the reference along the line
// refIdx = start + speed*dubIdx: a different-framerate dub master.
func speedDub(ref [][vhash.HashLen]byte, start, count int, speed float64) [][vhash.HashLen]byte {
	dub := make([][vhash.HashLen]byte, count)
	for j := range dub {
		dub[j] = ref[start+int(math.Round(speed*float64(j)))]
	}
	return dub
}

func TestFitTimingConstantOffset(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 4000)   // 200 s
	dub := hashesOf(600, 3000) // the same content from 30 s on: 150 s

	m, err := fitTiming(ref, dub, 200, 150, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments: got %d, want 1", len(m.Segments))
	}
	s := m.Segments[0]
	if math.Abs(m.Speed-1) > 0.001 {
		t.Errorf("speed: got %.5f, want 1", m.Speed)
	}
	if math.Abs(s.Intercept-30) > interval {
		t.Errorf("intercept: got %.3f, want 30", s.Intercept)
	}
	if m.NInliers != 3000 || m.NFine < 2990 || m.InlierFrac != 1 {
		t.Errorf("counts: %d of %d (frac %.2f)", m.NInliers, m.NFine, m.InlierFrac)
	}
	if math.Abs(m.Overlap-150) > 1 {
		t.Errorf("overlap: got %.2f, want 150", m.Overlap)
	}
	if m.SpanSupport < 0.99 {
		t.Errorf("span support: got %.3f", m.SpanSupport)
	}
	if m.MedianResid > interval/2 {
		t.Errorf("median residual: got %.3f", m.MedianResid)
	}
}

func TestFitTimingPALSpeedup(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 3652) // 182.6 s
	// a PAL-speedup dub starting 60 s into the reference: 4.27 % faster
	dub := speedDub(ref, 1200, 2352, 1.0427) // 117.6 s

	m, err := fitTiming(ref, dub, 182.6, 117.6, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments: got %d, want 1", len(m.Segments))
	}
	if math.Abs(m.Segments[0].Intercept-60) > 2*interval {
		t.Errorf("intercept: got %.3f, want 60", m.Segments[0].Intercept)
	}
	if math.Abs(m.Speed-1.0427) > 0.002 {
		t.Errorf("speed: got %.5f, want 1.0427", m.Speed)
	}
	if m.InlierFrac < 0.9 {
		t.Errorf("inlier fraction: got %.2f", m.InlierFrac)
	}
}

func TestFitTimingReversePAL(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 3000) // 150 s
	// the reference is the PAL-speeded side: the dub runs 4.1 % slower,
	// so it is longer than the reference
	dub := speedDub(ref, 0, 3128, 0.9590) // 156.4 s

	m, err := fitTiming(ref, dub, 150, 156.4, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(m.Segments[0].Intercept) > 2*interval {
		t.Errorf("intercept: got %.3f, want 0", m.Segments[0].Intercept)
	}
	if math.Abs(m.Speed-0.959) > 0.002 {
		t.Errorf("speed: got %.5f, want 0.959", m.Speed)
	}
}

func TestFitTimingLateStart(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 6000)    // 300 s
	dub := hashesOf(3000, 3000) // the dub starts 150 s into the reference

	m, err := fitTiming(ref, dub, 300, 150, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(m.Segments[0].Intercept-150) > interval {
		t.Errorf("intercept: got %.3f, want 150", m.Segments[0].Intercept)
	}
	if math.Abs(m.Speed-1) > 0.001 {
		t.Errorf("speed: got %.5f, want 1", m.Speed)
	}
}

func TestFitTimingStaticIntro(t *testing.T) {
	const interval = 0.05
	// both sides open with one static shot; the reference's intro is
	// twice as long. The static stretch must not position the map - the
	// body content must.
	static := randomHashes(rand.New(rand.NewSource(7)), 1)
	ref := append(dupHashes(static[0], 1200), hashesOf(1200, 2000)...) // 160 s
	dub := append(dupHashes(static[0], 600), hashesOf(1200, 2000)...)  // 130 s

	m, err := fitTiming(ref, dub, 160, 130, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(m.Segments[0].Intercept-30) > 2*interval {
		t.Errorf("intercept: got %.3f, want 30", m.Segments[0].Intercept)
	}
	if math.Abs(m.Speed-1) > 0.001 {
		t.Errorf("speed: got %.5f, want 1", m.Speed)
	}
}

func dupHashes(h [vhash.HashLen]byte, n int) [][vhash.HashLen]byte {
	out := make([][vhash.HashLen]byte, n)
	for i := range out {
		out[i] = h
	}
	return out
}

func TestFitTimingToleratesFalseMatches(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 3652)
	dub := speedDub(ref, 1200, 2352, 1.0427)
	// a fifth of the dub frames carry unrelated content (a different
	// scene, dropouts): the fit must come from the remaining majority
	rng := rand.New(rand.NewSource(3))
	for j := 0; j < len(dub); j += 5 {
		dub[j] = randomHashes(rng, 1)[0]
	}

	m, err := fitTiming(ref, dub, 182.6, 117.6, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(m.Segments[0].Intercept-60) > 2*interval {
		t.Errorf("intercept: got %.3f, want 60", m.Segments[0].Intercept)
	}
	if math.Abs(m.Speed-1.0427) > 0.002 {
		t.Errorf("speed: got %.5f, want 1.0427", m.Speed)
	}
	if m.InlierFrac < 0.7 {
		t.Errorf("inlier fraction: got %.2f, want the ~80%% matched majority", m.InlierFrac)
	}
}

func TestFitTimingTwoCuts(t *testing.T) {
	const interval = 0.05
	// a TV-trimmed dub: two scenes of 2 s removed mid-file. The map must
	// come out as three segments sharing the speed, with the offsets
	// stepping by the removed content and silent gaps at the cuts.
	ref := hashesOf(0, 8000) // 400 s
	dub := make([][vhash.HashLen]byte, 0, 7920)
	for j := 0; j < 7920; j++ {
		src := j
		if j >= 2000 {
			src += 40 // 2 s removed after dub 100 s
		}
		if j >= 6000 {
			src += 40 // another 2 s removed after dub 300 s
		}
		dub = append(dub, ref[src])
	}

	m, err := fitTiming(ref, dub, 400, 396, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if len(m.Segments) != 3 {
		t.Fatalf("segments: got %d, want 3", len(m.Segments))
	}
	for i, s := range m.Segments {
		if want := float64(i) * 2; math.Abs(s.Intercept-want) > 2*interval {
			t.Errorf("segment %d intercept: got %.3f, want %.1f", i+1, s.Intercept, want)
		}
		if s.Matches < 1500 {
			t.Errorf("segment %d: only %d matches", i+1, s.Matches)
		}
	}
	for i := 0; i+1 < len(m.Segments); i++ {
		gap := m.Segments[i+1].RefStart - m.Segments[i].RefEnd
		if math.Abs(gap-2) > 4*interval {
			t.Errorf("gap %d: got %.3f, want 2", i+1, gap)
		}
	}
	if m.InlierFrac < 0.9 {
		t.Errorf("inlier fraction: got %.2f", m.InlierFrac)
	}
}

func TestFitTimingRefusesMixedSpeed(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 6000) // 300 s
	// the second half of the dub runs at a different speed: a mixed
	// transfer, which the shared-speed model must refuse
	dub := append(speedDub(ref, 0, 3000, 1.0), speedDub(ref, 3000, 2700, 1.08)...)

	if _, err := fitTiming(ref, dub, 300, 285, interval, 45); err == nil {
		t.Fatal("a mixed-speed transfer must be refused")
	} else if !strings.Contains(err.Error(), "different speed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFitTimingRefusesUnrelated(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 400)
	dub := randomHashes(rand.New(rand.NewSource(2)), 360)

	if _, err := fitTiming(ref, dub, 20, 18, interval, 45); err == nil {
		t.Fatal("unrelated content must be refused")
	} else if !strings.Contains(err.Error(), "too few reliable matches") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateFitRules(t *testing.T) {
	const interval = 0.05
	good := &timingMap{
		Speed: 1.0427,
		Segments: []segment{{
			DubStart: 0, DubEnd: 105, RefStart: 60, RefEnd: 170,
			Slope: 1.0427, Intercept: 60, Matches: 5000, Residual: 0.02,
		}},
		NInliers:    5000,
		NFine:       5000,
		InlierFrac:  1,
		SpanSupport: 1,
		Overlap:     105,
		MedianResid: 0.02,
		P95Resid:    0.03,
		DurRatio:    1.0427,
	}
	if err := validateFit(good, 110, 105.3, interval); err != nil {
		t.Errorf("a solid PAL fit must pass: %v", err)
	}

	tooFew := *good
	tooFew.NInliers = 10
	if err := validateFit(&tooFew, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "too few reliable matches") {
		t.Errorf("too few inliers must refuse: %v", err)
	}

	fragmented := *good
	fragmented.Segments = nil
	for i := 0; i < 25; i++ {
		fragmented.Segments = append(fragmented.Segments, segment{
			DubStart: float64(i), DubEnd: float64(i) + 1, RefStart: float64(i), RefEnd: float64(i) + 1,
			Slope: 1, Intercept: 0, Matches: 100, Residual: 0.02,
		})
	}
	fragmented.NInliers = 2500
	fragmented.NFine = 2500
	if err := validateFit(&fragmented, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "fragmented") {
		t.Errorf("a fragmented map must refuse: %v", err)
	}

	speedOut := *good
	speedOut.Speed = 1.8
	if err := validateFit(&speedOut, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "outside the plausible range") {
		t.Errorf("an implausible speed must refuse: %v", err)
	}

	// the duration ratio is context only: a modified intro or credits can
	// flip its direction against the true PAL speedup, and that must not
	// refuse a content-derived fit
	edits := *good
	edits.DurRatio = 0.95
	if err := validateFit(&edits, 110, 105.3, interval); err != nil {
		t.Errorf("a duration ratio against the fitted speed must not refuse: %v", err)
	}
	editsSameSpeed := *good
	editsSameSpeed.Speed = 1.0
	editsSameSpeed.DurRatio = 1.08
	if err := validateFit(&editsSameSpeed, 110, 105.3, interval); err != nil {
		t.Errorf("an edited intro over same-speed content must not refuse: %v", err)
	}

	// a low share of hash-matching frames is a transfer-quality property:
	// only the sanity floor refuses
	weak := *good
	weak.InlierFrac = 0.3
	if err := validateFit(&weak, 110, 105.3, interval); err != nil {
		t.Errorf("a low hash-match share must not refuse: %v", err)
	}
	starved := *good
	starved.NInliers = 5000
	starved.NFine = 25000
	starved.InlierFrac = 0.2
	if err := validateFit(&starved, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "too few reliable matches") {
		t.Errorf("a map supported by a fifth of the span must refuse: %v", err)
	}

	short := *good
	short.SpanSupport = 0.4
	if err := validateFit(&short, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "cover only") {
		t.Errorf("weak span support must refuse: %v", err)
	}

	cut := *good
	cut.Overlap = 30
	if err := validateFit(&cut, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "share only") {
		t.Errorf("a small shared span must refuse: %v", err)
	}

	noisy := *good
	noisy.MedianResid = 0.3
	if err := validateFit(&noisy, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "median residual") {
		t.Errorf("a large median residual must refuse: %v", err)
	}

	scattered := *good
	scattered.P95Resid = 0.5
	if err := validateFit(&scattered, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "p95 residual") {
		t.Errorf("a large p95 residual must refuse: %v", err)
	}

	// one garbage segment must refuse even when the global median is fine
	garbage := *good
	garbage.Segments = []segment{
		{DubStart: 0, DubEnd: 100, RefStart: 60, RefEnd: 165, Slope: 1.0427, Intercept: 60, Matches: 5000, Residual: 0.02},
		{DubStart: 100, DubEnd: 105, RefStart: 166, RefEnd: 170, Slope: 1.0427, Intercept: 61, Matches: 100, Residual: 0.25},
	}
	if err := validateFit(&garbage, 110, 105.3, interval); err == nil || !strings.Contains(err.Error(), "segment 2") {
		t.Errorf("a garbage segment must refuse: %v", err)
	}
}

func TestSpeedNote(t *testing.T) {
	for speed, want := range map[float64]string{
		1.0:      "same speed",
		1.0427:   "PAL speedup 23.976->25",
		1.0417:   "PAL speedup 24->25",
		0.9590:   "PAL slowdown 25->23.976",
		1.2488:   "NTSC speedup 24->29.97",
		0.9876:   "",
		1.042711: "PAL speedup 23.976->25",
	} {
		if got := speedNote(speed); got != want {
			t.Errorf("speedNote(%.5f) = %q, want %q", speed, got, want)
		}
	}
}

func TestPairBaselineScales(t *testing.T) {
	long := []timedMatch{{dub: 0, ref: 0}, {dub: 5400, ref: 5630}}
	minB, maxB := pairBaseline(long, 0.05)
	if minB != baselineCap || maxB != 2*baselineCap {
		t.Errorf("long content baseline: got %.1f-%.1f, want the %.0fs cap window", minB, maxB, baselineCap)
	}
	short := []timedMatch{{dub: 0, ref: 0}, {dub: 17, ref: 17}}
	minB, maxB = pairBaseline(short, 0.2)
	if math.Abs(minB-1.7) > 1e-9 || maxB != 2*minB {
		t.Errorf("short content baseline: got %.1f-%.1f, want span/10 window", minB, maxB)
	}
}
