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

	f, err := fitTiming(ref, dub, 200, 150, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(f.Line.intercept-30) > interval {
		t.Errorf("intercept: got %.3f, want 30", f.Line.intercept)
	}
	if math.Abs(f.Line.speed-1) > 0.001 {
		t.Errorf("speed: got %.5f, want 1", f.Line.speed)
	}
	if f.NInliers != 3000 || f.NFine != 3000 || f.InlierFrac != 1 {
		t.Errorf("counts: %d of %d (frac %.2f)", f.NInliers, f.NFine, f.InlierFrac)
	}
	if math.Abs(f.Overlap-150) > 1e-6 {
		t.Errorf("overlap: got %.2f, want 150", f.Overlap)
	}
	if f.SpanSupport < 0.999 {
		t.Errorf("span support: got %.3f, want 1", f.SpanSupport)
	}
	if f.MedianResid > interval/2 {
		t.Errorf("median residual: got %.3f", f.MedianResid)
	}
}

func TestFitTimingPALSpeedup(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 3652) // 182.6 s
	// a PAL-speedup dub starting 60 s into the reference: 4.27 % faster
	dub := speedDub(ref, 1200, 2352, 1.0427) // 117.6 s

	f, err := fitTiming(ref, dub, 182.6, 117.6, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(f.Line.intercept-60) > 2*interval {
		t.Errorf("intercept: got %.3f, want 60", f.Line.intercept)
	}
	if math.Abs(f.Line.speed-1.0427) > 0.002 {
		t.Errorf("speed: got %.5f, want 1.0427", f.Line.speed)
	}
	if f.InlierFrac < 0.9 {
		t.Errorf("inlier fraction: got %.2f", f.InlierFrac)
	}
}

func TestFitTimingReversePAL(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 3000) // 150 s
	// the reference is the PAL-speeded side: the dub runs 4.1 % slower,
	// so it is longer than the reference
	dub := speedDub(ref, 0, 3128, 0.9590) // 156.4 s

	f, err := fitTiming(ref, dub, 150, 156.4, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(f.Line.intercept) > 2*interval {
		t.Errorf("intercept: got %.3f, want 0", f.Line.intercept)
	}
	if math.Abs(f.Line.speed-0.959) > 0.002 {
		t.Errorf("speed: got %.5f, want 0.959", f.Line.speed)
	}
}

func TestFitTimingLateStart(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 6000)    // 300 s
	dub := hashesOf(3000, 3000) // the dub starts 150 s into the reference

	f, err := fitTiming(ref, dub, 300, 150, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(f.Line.intercept-150) > interval {
		t.Errorf("intercept: got %.3f, want 150", f.Line.intercept)
	}
	if math.Abs(f.Line.speed-1) > 0.001 {
		t.Errorf("speed: got %.5f, want 1", f.Line.speed)
	}
}

func TestFitTimingStaticIntro(t *testing.T) {
	const interval = 0.05
	// both sides open with one static shot; the reference's intro is
	// twice as long. The static stretch must not position the line - the
	// body content must.
	static := randomHashes(rand.New(rand.NewSource(7)), 1)
	ref := append(dupHashes(static[0], 1200), hashesOf(1200, 2000)...) // 160 s
	dub := append(dupHashes(static[0], 600), hashesOf(1200, 2000)...)  // 130 s

	f, err := fitTiming(ref, dub, 160, 130, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(f.Line.intercept-30) > 2*interval {
		t.Errorf("intercept: got %.3f, want 30", f.Line.intercept)
	}
	if math.Abs(f.Line.speed-1) > 0.001 {
		t.Errorf("speed: got %.5f, want 1", f.Line.speed)
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

	f, err := fitTiming(ref, dub, 182.6, 117.6, interval, 45)
	if err != nil {
		t.Fatalf("fitTiming: %v", err)
	}
	if math.Abs(f.Line.intercept-60) > 2*interval {
		t.Errorf("intercept: got %.3f, want 60", f.Line.intercept)
	}
	if math.Abs(f.Line.speed-1.0427) > 0.002 {
		t.Errorf("speed: got %.5f, want 1.0427", f.Line.speed)
	}
	if f.InlierFrac < 0.7 {
		t.Errorf("inlier fraction: got %.2f, want the ~80%% matched majority", f.InlierFrac)
	}
}

func TestFitTimingRefusesTwoOffsets(t *testing.T) {
	const interval = 0.05
	ref := hashesOf(0, 2000) // 100 s
	// the dub's second half follows a different offset: a mid-video edit,
	// which the model deliberately does not support
	dub := append(hashesOf(0, 500), hashesOf(1500, 500)...)

	if _, err := fitTiming(ref, dub, 100, 50, interval, 45); err == nil {
		t.Fatal("a mid-video edit must be refused")
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
	good := &timingFit{
		Line:        line{speed: 1.0427, intercept: 60},
		NInliers:    5000,
		NFine:       5000,
		InlierFrac:  1,
		SpanSupport: 0.99,
		Overlap:     100,
		MedianResid: 0.02,
		DurRatio:    1.0427,
	}
	if err := validateFit(good, 170, 105.3, interval); err != nil {
		t.Errorf("a solid PAL fit must pass: %v", err)
	}

	tooFew := *good
	tooFew.NInliers = 10
	if err := validateFit(&tooFew, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "too few reliable matches") {
		t.Errorf("too few inliers must refuse: %v", err)
	}

	speedOut := *good
	speedOut.Line.speed = 1.8
	if err := validateFit(&speedOut, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "outside the plausible range") {
		t.Errorf("an implausible speed must refuse: %v", err)
	}

	// the duration ratio is context only: a modified intro or credits can
	// flip its direction against the true PAL speedup, and that must not
	// refuse a content-derived fit
	edits := *good
	edits.DurRatio = 0.95
	if err := validateFit(&edits, 170, 105.3, interval); err != nil {
		t.Errorf("a duration ratio against the fitted speed must not refuse: %v", err)
	}
	editsSameSpeed := *good
	editsSameSpeed.Line.speed = 1.0
	editsSameSpeed.DurRatio = 1.08
	if err := validateFit(&editsSameSpeed, 170, 105.3, interval); err != nil {
		t.Errorf("an edited intro over same-speed content must not refuse: %v", err)
	}

	weak := *good
	weak.InlierFrac = 0.3
	if err := validateFit(&weak, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "follow one timing line") {
		t.Errorf("a low inlier fraction must refuse: %v", err)
	}

	short := *good
	short.SpanSupport = 0.4
	if err := validateFit(&short, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "cover only") {
		t.Errorf("weak span support must refuse: %v", err)
	}

	cut := *good
	cut.Overlap = 30
	if err := validateFit(&cut, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "share only") {
		t.Errorf("a small shared span must refuse: %v", err)
	}

	noisy := *good
	noisy.MedianResid = 0.3
	if err := validateFit(&noisy, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "median residual") {
		t.Errorf("a large median residual must refuse: %v", err)
	}

	scattered := *good
	scattered.P95Resid = 0.5
	if err := validateFit(&scattered, 170, 105.3, interval); err == nil || !strings.Contains(err.Error(), "p95 residual") {
		t.Errorf("a large p95 residual must refuse: %v", err)
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
	if got := pairBaseline(long, 0.05); got != 30 {
		t.Errorf("long content baseline: got %v, want 30", got)
	}
	short := []timedMatch{{dub: 0, ref: 0}, {dub: 17, ref: 17}}
	if got := pairBaseline(short, 0.2); math.Abs(got-17.0/8) > 1e-9 {
		t.Errorf("short content baseline: got %v, want span/8", got)
	}
}
