package align

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/hranicka/mediatool/internal/vhash"
)

// The align timing model: the two files are the same edit up to one
// global speed difference (dub masters often run at PAL speedup or an
// NTSC conversion) and one time shift (a shorter or longer intro or
// credits). Everything is explained by one affine line,
//
//	ref_t = intercept + speed × dub_t
//
// fitted robustly from perceptual frame matches: a coarse global pass
// locates the line, constrained walks refine it at the full sampling
// grid, and the final fit keeps only the matches that agree with the
// line. Mid-video scene cuts between the two files are out of scope by
// design; when the matches do not support one line, the fit is refused
// instead of producing wrong lipsync.

const (
	// coarseDist is the max Hamming distance (of 1024 bits) for a coarse
	// candidate match. The coarse fit only needs a plurality of matches
	// on the true line, so the cut is generous, but unrelated frames sit
	// hundreds of bits apart and never reach it.
	coarseDist = 100
	// coarseCap bounds the coarse pass cost: at most this many dub
	// samples are matched against an equally strided reference, whatever
	// the file length. Shorter files keep the full sampling grid.
	coarseCap = 12000
	// refineWindow is how far (seconds) around the coarse line the strided
	// walk searches - it must cover the coarse fit's slope uncertainty at
	// the file ends. fineWindow is the tighter window around the refined
	// line for the every-frame walk.
	refineWindow = 5.0
	fineWindow   = 1.0
	// exhaustivePairs is the match count below which the Theil-Sen slope
	// enumerates all qualifying pairs (exact, deterministic); above it,
	// pairs are sampled randomly (seeded, so runs stay reproducible).
	exhaustivePairs = 300
	// maxPairs caps the sampled slope pairs per Theil-Sen fit and minPairs
	// is the smallest number of qualifying pairs a fit accepts.
	maxPairs = 200000
	minPairs = 20
	// slopeMin/slopeMax bound a physically plausible pair slope; anything
	// else is a false pair (e.g. two matches inside one static stretch).
	slopeMin = 0.5
	slopeMax = 2.0
	// trimResidual is how many sampling intervals a match may sit off the
	// line in the final fit before it is dropped as an outlier.
	trimResidual = 3.0
	// ambiguousHash is how many coarse samples on one side may share a
	// hash before that content counts as position-ambiguous (static
	// scenes, logos, black frames) and is dropped from the coarse stage:
	// a match inside such a stretch says nothing about where in it the
	// frame belongs.
	ambiguousHash = 5

	// Validation rules (validateFit). Coverage is measured on the shared
	// span under the fitted line, never on the full durations: a modified
	// intro or credits legitimately leaves the ends unmatched.
	minInliers     = 60
	minInlierFrac  = 0.5
	minSpanSupport = 0.75
	minOverlap     = 0.6
	warnOverlap    = 0.8
	maxMedianResid = 2.0 // × interval
	maxP95Resid    = 4.0 // × interval
	// speedMin/speedMax bound the fitted speed: PAL speedup in both
	// directions (1.0427 / 0.9590) and NTSC 24→29.97 transfers (1.2488 /
	// 0.8008) with margin.
	speedMin = 0.75
	speedMax = 1.30
	// directionTol is how far the fitted speed and the duration ratio may
	// sit from 1 before the report remarks on their disagreement. The
	// ratio is only context: a modified intro or credits shifts it by a
	// few percent and can flip its direction against the true speed.
	directionTol = 0.03
)

// line maps dubbed time to reference time.
type line struct {
	speed     float64 // reference seconds per dubbed second
	intercept float64 // reference time where dub time 0 lands
}

func (l line) refAt(dub float64) float64 { return l.intercept + l.speed*dub }

func (l line) dubAt(ref float64) float64 { return (ref - l.intercept) / l.speed }

// timedMatch is one frame match in seconds on the sampling grid.
type timedMatch struct {
	dub, ref float64
}

// timingFit is the align result: one affine line plus the evidence that
// supports it.
type timingFit struct {
	Line        line
	NCoarse     int     // coarse candidate matches that located the line
	NInliers    int     // fine matches the final line keeps
	NFine       int     // dub grid positions sampled inside the shared span
	InlierFrac  float64 // NInliers of NFine
	SpanSupport float64 // first-to-last inlier span vs the shared span
	Overlap     float64 // shared span in reference seconds
	MedianResid float64
	P95Resid    float64
	DurRatio    float64 // refDur/dubDur, context for the report and guardrail
}

// fitTiming fits the timing line from the two files' frame hashes: a
// coarse global match locates the line, a strided walk constrained to it
// refines the fit at the full sampling grid, an every-frame walk matches
// all dub frames around the refined line, and the final robust fit keeps
// only the supporting matches. The result is validated against the
// durations before it is returned.
func fitTiming(ref, dub [][vhash.HashLen]byte, refDur, dubDur, interval float64, matchDist int) (*timingFit, error) {
	if len(ref) == 0 || len(dub) == 0 {
		return nil, fmt.Errorf("no frames extracted (reference %d, dubbed %d)", len(ref), len(dub))
	}
	rng := rand.New(rand.NewSource(1))
	stride := max(1, (len(dub)+coarseCap-1)/coarseCap)

	coarse := coarseMatches(ref, dub, stride, interval)
	if len(coarse) < 2 {
		return nil, tooFewError(len(coarse))
	}
	l, ok := fitLine(coarse, interval, rng)
	if !ok {
		return nil, tooFewError(len(coarse))
	}

	refined := walkLine(ref, dub, l, interval, matchDist, refineWindow, refDur, stride)
	if len(refined) < 2 {
		return nil, tooFewError(len(refined))
	}
	l, ok = fitLine(refined, interval, rng)
	if !ok {
		return nil, tooFewError(len(refined))
	}

	fine := walkLine(ref, dub, l, interval, matchDist, fineWindow, refDur, 1)
	if len(fine) < 2 {
		return nil, tooFewError(len(fine))
	}
	var inliers []timedMatch
	l, inliers, ok = fitRobust(fine, interval, rng)
	if !ok {
		return nil, tooFewError(len(fine))
	}

	f := &timingFit{Line: l, NCoarse: len(coarse), NInliers: len(inliers)}
	if dubDur > 0 {
		f.DurRatio = refDur / dubDur
	}

	dubLo := max(0.0, l.dubAt(0))
	dubHi := min(dubDur, l.dubAt(refDur))
	if dubHi > dubLo {
		f.Overlap = l.speed * (dubHi - dubLo)
		jLo := max(0, int(math.Ceil(dubLo/interval)))
		jHi := min(len(dub)-1, int(math.Floor(dubHi/interval)))
		f.NFine = max(0, jHi-jLo+1)
	}
	if f.NFine > 0 {
		f.InlierFrac = float64(len(inliers)) / float64(f.NFine)
	}
	if f.Overlap > 0 && len(inliers) >= 2 {
		f.SpanSupport = min(1, (inliers[len(inliers)-1].ref-inliers[0].ref)/f.Overlap)
	}
	resid := make([]float64, len(inliers))
	for i, p := range inliers {
		resid[i] = math.Abs(p.ref - l.refAt(p.dub))
	}
	f.MedianResid = medianFloat(resid)
	f.P95Resid = percentile(resid, 0.95)

	if err := validateFit(f, refDur, dubDur, interval); err != nil {
		return nil, err
	}
	return f, nil
}

// validateFit refuses a fit the matches do not support: too few or too
// inconsistent matches, a speed outside the plausible conversions, or a
// shared span too small for the files to be the same edit. The duration
// ratio is reported alongside but never refuses: a modified intro or
// credits can flip it against the true speed direction.
func validateFit(f *timingFit, refDur, dubDur, interval float64) error {
	if f.NInliers < minInliers || f.NInliers*4 < f.NFine {
		return tooFewError(f.NInliers)
	}
	if f.Line.speed < speedMin || f.Line.speed > speedMax {
		return fmt.Errorf("fitted speed %.4f is outside the plausible range [%.2f, %.2f] - the transfers do not share a known speed conversion",
			f.Line.speed, speedMin, speedMax)
	}
	if f.InlierFrac < minInlierFrac {
		return fmt.Errorf("only %.0f%% of the frames in the shared span follow one timing line (%d of %d)",
			f.InlierFrac*100, f.NInliers, f.NFine)
	}
	if f.SpanSupport < minSpanSupport {
		return fmt.Errorf("matches cover only %.0f%% of the shared span - the timing is not established across the content",
			f.SpanSupport*100)
	}
	if shortest := math.Min(refDur, dubDur); f.Overlap < minOverlap*shortest {
		return fmt.Errorf("the files share only %.0f%% of their content - they are likely different cuts",
			f.Overlap/shortest*100)
	}
	if f.MedianResid > maxMedianResid*interval {
		return fmt.Errorf("the matches disagree on one speed and offset: median residual %dms (limit %dms) - no consistent timing found",
			int(math.Round(f.MedianResid*1000)), int(math.Round(maxMedianResid*interval*1000)))
	}
	if f.P95Resid > maxP95Resid*interval {
		return fmt.Errorf("the matches scatter too far off one timing line: p95 residual %dms (limit %dms)",
			int(math.Round(f.P95Resid*1000)), int(math.Round(maxP95Resid*interval*1000)))
	}
	return nil
}

// coarseMatches pairs every stride-th dub frame with its best global
// reference match (brute force over the equally strided reference). There
// is no walk state: each dub frame is matched independently, so one bad
// frame cannot influence the next. Matches inside position-ambiguous
// content (a hash repeated on either side) are dropped - they cannot say
// where within that stretch the frame belongs.
func coarseMatches(ref, dub [][vhash.HashLen]byte, stride int, interval float64) []timedMatch {
	refCount := hashCounts(ref, stride)
	dubCount := hashCounts(dub, stride)
	pts := make([]timedMatch, 0, len(dub)/stride+1)
	for j := 0; j < len(dub); j += stride {
		if dubCount[dub[j]] >= ambiguousHash {
			continue
		}
		best, dist := -1, coarseDist+1
		for k := 0; k < len(ref); k += stride {
			if d := vhash.Hamming(dub[j], ref[k]); d < dist {
				best, dist = k, d
			}
		}
		if best >= 0 && refCount[ref[best]] < ambiguousHash {
			pts = append(pts, timedMatch{dub: float64(j) * interval, ref: float64(best) * interval})
		}
	}
	return pts
}

// hashCounts counts how often each hash occurs among the strided samples.
func hashCounts(hashes [][vhash.HashLen]byte, stride int) map[[vhash.HashLen]byte]int {
	counts := make(map[[vhash.HashLen]byte]int, len(hashes)/stride+1)
	for j := 0; j < len(hashes); j += stride {
		counts[hashes[j]]++
	}
	return counts
}

// walkLine matches dub frames against a window around the line's
// prediction. Every dub frame (or every stride-th one) is matched
// independently, so a false match costs one discarded point and cannot
// derail a sequence. Positions predicted more than one window outside the
// reference have no counterpart; right at the edge the search center is
// clamped, so a slightly misplaced prediction still finds its match.
func walkLine(ref, dub [][vhash.HashLen]byte, l line, interval float64, matchDist int, window, refDur float64, stride int) []timedMatch {
	w := int(math.Round(window / interval))
	matches := make([]timedMatch, 0, len(dub)/stride+1)
	for j := 0; j < len(dub); j += stride {
		pred := l.refAt(float64(j) * interval)
		if pred < -window || pred > refDur+window {
			continue
		}
		center := min(max(int(math.Round(pred/interval)), 0), len(ref)-1)
		best, dist := vhash.WindowBest(dub, ref, j, max(0, center-w), min(len(ref), center+w+1), center)
		if dist < matchDist {
			matches = append(matches, timedMatch{dub: float64(j) * interval, ref: float64(best) * interval})
		}
	}
	return matches
}

// theilSenSlope estimates ref-seconds per dub-second as the median slope
// over match pairs at least minBaseline apart. Long baselines make the
// grid quantization noise negligible per pair, and the median absorbs
// false matches: the estimator tolerates a large minority of them.
func theilSenSlope(pts []timedMatch, minBaseline float64, rng *rand.Rand) (float64, bool) {
	n := len(pts)
	if n < 2 {
		return 0, false
	}
	var slopes []float64
	if n <= exhaustivePairs {
		slopes = make([]float64, 0, n*(n-1)/2)
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				if s, ok := pairSlope(pts[i], pts[j], minBaseline); ok {
					slopes = append(slopes, s)
				}
			}
		}
	} else {
		slopes = make([]float64, 0, maxPairs)
		for k := 0; k < maxPairs; k++ {
			i, j := rng.Intn(n), rng.Intn(n)
			if i == j {
				continue
			}
			if s, ok := pairSlope(pts[i], pts[j], minBaseline); ok {
				slopes = append(slopes, s)
			}
		}
	}
	if len(slopes) < minPairs {
		return 0, false
	}
	return medianFloat(slopes), true
}

// pairSlope is the slope of one match pair, accepted when the dub
// baseline is long enough and the slope physically plausible.
func pairSlope(a, b timedMatch, minBaseline float64) (float64, bool) {
	dd := b.dub - a.dub
	if math.Abs(dd) < minBaseline {
		return 0, false
	}
	s := (b.ref - a.ref) / dd
	if s <= slopeMin || s >= slopeMax {
		return 0, false
	}
	return s, true
}

// pairBaseline picks the minimum pair baseline for slope estimation: long
// enough to keep the per-pair quantization noise small, but never more
// than an eighth of the match span so short files still fit. pts must be
// sorted by dub time.
func pairBaseline(pts []timedMatch, interval float64) float64 {
	if len(pts) < 2 {
		return 0
	}
	span := pts[len(pts)-1].dub - pts[0].dub
	if span <= 0 {
		return 0
	}
	b := span / 8
	if b > 30 {
		b = 30
	}
	return max(b, 6*interval)
}

// medianIntercept fits the intercept as the median of ref - speed*dub,
// which is not dragged around by a few bad matches.
func medianIntercept(pts []timedMatch, speed float64) float64 {
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = p.ref - speed*p.dub
	}
	return medianFloat(vals)
}

// fitLine fits one line through matches: Theil-Sen speed over
// long-baseline pairs, median intercept. ok is false when too few pairs
// qualify. No outlier trimming happens here - the callers are
// intermediate stages whose matches are already window-constrained; only
// fitRobust trims.
func fitLine(pts []timedMatch, interval float64, rng *rand.Rand) (line, bool) {
	speed, ok := theilSenSlope(pts, pairBaseline(pts, interval), rng)
	if !ok {
		return line{}, false
	}
	return line{speed: speed, intercept: medianIntercept(pts, speed)}, true
}

// fitRobust fits the final line: Theil-Sen speed and median intercept,
// matches farther than trimResidual intervals off the line dropped,
// iterated. It returns the inliers that define the line.
func fitRobust(pts []timedMatch, interval float64, rng *rand.Rand) (line, []timedMatch, bool) {
	l, ok := fitLine(pts, interval, rng)
	if !ok {
		return line{}, nil, false
	}
	for iter := 0; iter < 3; iter++ {
		kept := make([]timedMatch, 0, len(pts))
		for _, p := range pts {
			if math.Abs(p.ref-l.refAt(p.dub)) <= trimResidual*interval {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(pts) {
			break
		}
		pts = kept
		if len(pts) < 2 {
			break
		}
		if speed, ok := theilSenSlope(pts, pairBaseline(pts, interval), rng); ok {
			l.speed = speed
		}
		l.intercept = medianIntercept(pts, l.speed)
	}
	return l, pts, true
}

// knownSpeeds labels the fitted speed when it sits within half a
// thousandth of a common transfer conversion; the gaps between the known
// ratios are at least 0.001, so the labels do not collide.
var knownSpeeds = []struct {
	ratio float64
	label string
}{
	{1.0427, "PAL speedup 23.976->25"},
	{1.0417, "PAL speedup 24->25"},
	{1.2488, "NTSC speedup 24->29.97"},
	{0.9590, "PAL slowdown 25->23.976"},
	{0.9600, "PAL slowdown 25->24"},
	{0.8008, "NTSC slowdown 29.97->24"},
	{1.0, "same speed"},
}

// speedNote names a known speed conversion, or "" for an unnamed ratio.
func speedNote(speed float64) string {
	for _, s := range knownSpeeds {
		if math.Abs(speed-s.ratio) < 0.0005 {
			return s.label
		}
	}
	return ""
}

// tooFewError is the refusal for content that does not establish a line.
func tooFewError(n int) error {
	return fmt.Errorf("too few reliable matches (%d) to establish one timing line - the files likely are not the same edit", n)
}

// percentile returns the q-quantile of v (nearest rank).
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(float64(len(s)-1)*q)]
}

func medianFloat(v []float64) float64 {
	s := make([]float64, len(v))
	copy(s, v)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
