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
// NTSC conversion), one time shift (a shorter or longer intro or
// credits), and a few mid-video cuts — TV broadcasts routinely trim
// scenes to fit their timeslot, which shifts the offset for the rest of
// the file. Everything is explained by a piecewise map whose segments
// share one speed and differ only in offset:
//
//	ref_t = intercept_i + speed × dub_t   on segment i
//
// fitted robustly from perceptual frame matches: a coarse global pass
// estimates the speed and locates the offset jumps, constrained walks
// refine the segments at the full sampling grid, and the final fit keeps
// only the matches that agree with their segment. The gaps between
// segments are real edits: the reference region of a removed scene has
// no dub counterpart and stays silent. When the matches do not support
// the map, the fit is refused instead of producing wrong lipsync.

const (
	// coarseDist is the max Hamming distance (of 1024 bits) for a coarse
	// candidate match. The coarse pass only needs a plurality of matches
	// on the true correspondence, so the cut is generous, but unrelated
	// frames sit hundreds of bits apart and never reach it.
	coarseDist = 100
	// coarseCap bounds the coarse pass cost: at most this many dub
	// samples are matched against an equally strided reference, whatever
	// the file length. Shorter files keep the full sampling grid.
	coarseCap = 12000
	// segWindow is how far (seconds) around its segment's line the
	// every-frame walk searches.
	segWindow = 1.5
	// exhaustivePairs is the match count below which a Theil-Sen fit
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
	// baselineCap bounds the Theil-Sen pair baseline from above. A pair
	// spanning a mid-video cut is inflated by cut/baseline - one-sided,
	// so when long baselines make most pairs span a cut, the median is
	// dragged up. Pairs are kept in a narrow short window instead: short
	// enough that most pairs are cut-free (the inflated ones then sit
	// far outside the noise band and the median ignores them), long
	// enough to keep the per-pair quantization noise acceptable.
	baselineCap = 25.0
	// jumpSplit is the offset level shift (seconds) between match
	// buckets that opens a new segment: a mid-video cut. False splits
	// are harmless (both intercepts come out equal), missed splits leave
	// permanent drift, so the fine threshold sits at the edge of
	// perception (an undetected cut leaves at most half of it as error
	// after the median intercept) while staying far above the bucket
	// noise (~2 ms at the fine stage).
	jumpSplitCoarse = 0.3
	jumpSplitFine   = 0.1
	// maxTilt is the offset difference (seconds) between the first and
	// second half of one segment above which the segment runs at a
	// different speed than the rest of the file - a mixed transfer,
	// which the shared-speed model refuses loudly.
	maxTilt = 0.15
	// minBucketMatches is the smallest match count a segment-detection
	// bucket needs to vote on the offset level; sparse buckets stay in
	// the current segment.
	minBucketMatches = 8
	// trimResidual is how many sampling intervals a match may sit off
	// its segment's line before it is dropped as an outlier.
	trimResidual = 3.0
	// minSegmentMatches is the smallest match count a segment needs to
	// enter the map, and maxSegments refuses fragmented maps.
	minSegmentMatches = 25
	maxSegments       = 24

	// Validation rules (validateFit). Coverage is measured on the spans
	// the segments cover, never on the full durations: a modified intro
	// or credits legitimately leaves the ends unmatched. The share of dub
	// frames that hash-match at all is a property of the transfer quality
	// (a rough TV capture matches half its frames), not of the map, so it
	// is reported but only a low sanity floor refuses.
	minInliers     = 60
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

// segLine is a line valid on one dub span, used to route the constrained
// walks. Segments share the speed and differ only in intercept.
type segLine struct {
	line
	DubStart, DubEnd float64
}

// matchRange is a [from, to) index range into a sorted match slice.
type matchRange struct{ from, to int }

// matchBucket groups consecutive matches of a split-segment bucket.
type matchBucket struct {
	from, to int
	median   float64
	empty    bool
}

// timedMatch is one frame match in seconds on the sampling grid.
type timedMatch struct {
	dub, ref float64
}

// segment is one mapped span of the final timing map: reference time in
// [RefStart, RefEnd) plays dub audio from [DubStart, DubEnd) through the
// segment's line. All segments share one speed; the reference gaps
// between segments are edits (removed scenes) and stay silent.
type segment struct {
	DubStart, DubEnd float64
	RefStart, RefEnd float64
	Slope            float64
	Intercept        float64
	Matches          int
	Residual         float64 // median absolute match residual in seconds
}

func (s segment) refAt(dub float64) float64 { return s.Intercept + s.Slope*dub }

func (s segment) dubAt(ref float64) float64 { return (ref - s.Intercept) / s.Slope }

// timingMap is the align result: the piecewise map plus the evidence that
// supports it.
type timingMap struct {
	Speed       float64 // shared global speed, reference seconds per dub second
	Segments    []segment
	NCoarse     int     // coarse candidate matches that located the map
	NInliers    int     // fine matches the final map keeps
	NFine       int     // dub grid positions inside the segments' dub spans
	InlierFrac  float64 // NInliers of NFine
	SpanSupport float64 // covered reference span vs the reference duration
	Overlap     float64 // covered dub span in seconds
	MedianResid float64
	P95Resid    float64
	DurRatio    float64 // refDur/dubDur, context for the report
}

// fitTiming fits the timing map from the two files' frame hashes: a
// coarse global match estimates the speed and locates the offset jumps
// (mid-video cuts), constrained walks refine the segments at the full
// sampling grid, and the final robust fit keeps only the supporting
// matches. The result is validated against the durations before it is
// returned.
func fitTiming(ref, dub [][vhash.HashLen]byte, refDur, dubDur, interval float64, matchDist int) (*timingMap, error) {
	if len(ref) == 0 || len(dub) == 0 {
		return nil, fmt.Errorf("no frames extracted (reference %d, dubbed %d)", len(ref), len(dub))
	}
	rng := rand.New(rand.NewSource(1))
	stride := max(1, (len(dub)+coarseCap-1)/coarseCap)

	coarse := coarseMatches(ref, dub, stride, interval)
	if len(coarse) < 2 {
		return nil, tooFewError(len(coarse))
	}
	base, baseMax := pairBaseline(coarse, interval)
	speed, ok := theilSenSlope(coarse, base, baseMax, rng)
	if !ok {
		return nil, tooFewError(len(coarse))
	}

	groups := splitSegments(coarse, speed, interval, jumpSplitCoarse)
	if len(groups) == 0 {
		return nil, tooFewError(len(coarse))
	}
	if err := checkTilt(groups, speed, math.Max(0.2, 8e-4*dubDur/2)); err != nil {
		return nil, err
	}
	segs := segLines(groups, speed, dubDur)

	var fine []timedMatch
	for iter := 0; iter < 2; iter++ {
		fine = walkLine(ref, dub, segs, interval, matchDist, segWindow, refDur, 1)
		if len(fine) < 2 {
			return nil, tooFewError(len(fine))
		}
		if s, ok := segmentSlopes(groups, interval, rng); ok {
			speed = s
		}
		groups = splitSegments(fine, speed, interval, jumpSplitFine)
		if len(groups) == 0 {
			return nil, tooFewError(len(fine))
		}
		segs = segLines(groups, speed, dubDur)
	}

	m, err := buildMap(groups, speed, interval, refDur, dubDur, rng)
	if err != nil {
		return nil, err
	}
	m.NCoarse = len(coarse)
	m.DurRatio = 0
	if dubDur > 0 {
		m.DurRatio = refDur / dubDur
	}
	if err := validateFit(m, refDur, dubDur, interval); err != nil {
		return nil, err
	}
	return m, nil
}

// validateFit refuses a map the matches do not support: too few or too
// inconsistent matches, fragmentation, a speed outside the plausible
// conversions, or spans too small for the files to be the same edit. The
// duration ratio is reported alongside but never refuses: a modified
// intro or credits can flip it against the true speed direction.
func validateFit(m *timingMap, refDur, dubDur, interval float64) error {
	if m.NInliers < minInliers || m.NInliers*4 < m.NFine {
		return tooFewError(m.NInliers)
	}
	if len(m.Segments) > maxSegments {
		return fmt.Errorf("the timing fragmented into %d segments - the files likely are not the same edit", len(m.Segments))
	}
	if m.Speed < speedMin || m.Speed > speedMax {
		return fmt.Errorf("fitted speed %.4f is outside the plausible range [%.2f, %.2f] - the transfers do not share a known speed conversion",
			m.Speed, speedMin, speedMax)
	}
	if m.SpanSupport < minSpanSupport {
		return fmt.Errorf("the segments cover only %.0f%% of the shared span - the timing is not established across the content",
			m.SpanSupport*100)
	}
	if shortest := math.Min(refDur, dubDur); m.Overlap < minOverlap*shortest {
		return fmt.Errorf("the files share only %.0f%% of their content - they are likely different cuts",
			m.Overlap/shortest*100)
	}
	if m.MedianResid > maxMedianResid*interval {
		return fmt.Errorf("the matches disagree on one speed and offset: median residual %dms (limit %dms) - no consistent timing found",
			int(math.Round(m.MedianResid*1000)), int(math.Round(maxMedianResid*interval*1000)))
	}
	for i, s := range m.Segments {
		if s.Residual > maxMedianResid*interval {
			return fmt.Errorf("segment %d does not hold one offset: median residual %dms (limit %dms) - the timing match is not reliable there",
				i+1, int(math.Round(s.Residual*1000)), int(math.Round(maxMedianResid*interval*1000)))
		}
	}
	if m.P95Resid > maxP95Resid*interval {
		return fmt.Errorf("the matches scatter too far off the timing map: p95 residual %dms (limit %dms)",
			int(math.Round(m.P95Resid*1000)), int(math.Round(maxP95Resid*interval*1000)))
	}
	return nil
}

// checkTilt refuses a segment whose first and second halves sit at
// different offsets: within one segment the speed must be constant, and
// a tilt beyond limit means the segment runs at a different speed than
// the rest of the file (a mixed transfer). The limit scales with the
// span at the coarse stage, where the slope estimate is least precise.
func checkTilt(groups [][]timedMatch, speed, limit float64) error {
	for i, g := range groups {
		half := len(g) / 2
		if half < 10 {
			continue
		}
		tilt := math.Abs(medianOffset(g[:half], speed) - medianOffset(g[half:], speed))
		if tilt > limit {
			return fmt.Errorf("segment %d runs at a different speed than the rest of the file (offset tilt %.0fms) - mixed transfers are not supported",
				i+1, tilt*1000)
		}
	}
	return nil
}

// coarseMatches pairs every stride-th dub frame with its best global
// reference match (brute force over the equally strided reference). There
// is no walk state: each dub frame is matched independently, so one bad
// frame cannot influence the next, and the matches cover the whole file
// across mid-video cuts. Matches inside position-ambiguous content (a
// hash repeated on either side) are dropped - they cannot say where
// within that stretch the frame belongs.
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

// ambiguousHash is how many coarse samples on one side may share a hash
// before that content counts as position-ambiguous (static scenes,
// logos, black frames, held animation frames) and is dropped from the
// coarse stage: a match inside such a stretch says nothing about where
// in it the frame belongs, and the scan's first-occurrence preference
// would bias the match position systematically.
const ambiguousHash = 2

// hashCounts counts how often each hash occurs among the strided samples.
func hashCounts(hashes [][vhash.HashLen]byte, stride int) map[[vhash.HashLen]byte]int {
	counts := make(map[[vhash.HashLen]byte]int, len(hashes)/stride+1)
	for j := 0; j < len(hashes); j += stride {
		counts[hashes[j]]++
	}
	return counts
}

// walkLine matches dub frames against a window around their segment's
// prediction. Every dub frame is matched independently, so a false match
// costs one discarded point and cannot derail a sequence. Dub positions
// outside every segment (dropped heads, inserts) and predictions more
// than one window outside the reference have no counterpart; right at
// the edge the search center is clamped, so a slightly misplaced
// prediction still finds its match.
func walkLine(ref, dub [][vhash.HashLen]byte, segs []segLine, interval float64, matchDist int, window, refDur float64, stride int) []timedMatch {
	w := int(math.Round(window / interval))
	matches := make([]timedMatch, 0, len(dub)/stride+1)
	si := 0
	for j := 0; j < len(dub); j += stride {
		t := float64(j) * interval
		for si < len(segs) && t >= segs[si].DubEnd {
			si++
		}
		if si >= len(segs) || t < segs[si].DubStart {
			continue
		}
		pred := segs[si].refAt(t)
		if pred < -window || pred > refDur+window {
			continue
		}
		center := min(max(int(math.Round(pred/interval)), 0), len(ref)-1)
		best, dist := vhash.WindowBest(dub, ref, j, max(0, center-w), min(len(ref), center+w+1), center)
		if dist < matchDist {
			matches = append(matches, timedMatch{dub: t, ref: float64(best) * interval})
		}
	}
	return matches
}

// splitSegments groups matches into offset runs sharing one speed: it
// buckets them by dub time, takes each bucket's median offset
// (ref − slope·dub), and starts a new segment wherever the level shifts
// by more than jumpSplit and the shift is sustained by a later bucket —
// the signature of a mid-video cut. The boundary between two segments is
// then refined to the match where the offset actually switches, so the
// silent gap lands on the real edit, not on a bucket edge. pts must be
// sorted by dub time.
func splitSegments(pts []timedMatch, slope, interval float64, jumpSplit float64) [][]timedMatch {
	if len(pts) < 2 {
		return nil
	}
	span := pts[len(pts)-1].dub - pts[0].dub
	if span <= 0 {
		return [][]timedMatch{pts}
	}
	width := math.Max(8, span/80)
	bucket := func(t float64) int { return int(t / width) }

	// bucketize, keeping each bucket's median offset
	var buckets []matchBucket
	for start := 0; start < len(pts); {
		end := start
		for end < len(pts) && bucket(pts[end].dub) == bucket(pts[start].dub) {
			end++
		}
		b := matchBucket{from: start, to: end, empty: true}
		if end-start >= minBucketMatches {
			offs := make([]float64, 0, end-start)
			for _, p := range pts[start:end] {
				offs = append(offs, p.ref-slope*p.dub)
			}
			b.median, b.empty = medianFloat(offs), false
		}
		buckets = append(buckets, b)
		start = end
	}

	// walk the bucket levels; a shift is confirmed once a later bucket
	// agrees with the candidate level
	var ranges []matchRange
	bounds := make([]int, 0, 8) // bucket index where each confirmed range starts
	level := math.NaN()
	segBucket := 0
	var pendingBucket int
	pendingSet := false
	var pendingOff float64
	for bi, b := range buckets {
		if b.empty {
			continue
		}
		switch {
		case math.IsNaN(level):
			level = b.median
		case math.Abs(b.median-level) <= jumpSplit:
			pendingSet = false
		case !pendingSet:
			pendingBucket, pendingOff, pendingSet = bi, b.median, true
		case math.Abs(b.median-pendingOff) <= jumpSplit:
			ranges = append(ranges, matchRange{buckets[segBucket].from, buckets[pendingBucket].from})
			bounds = append(bounds, segBucket)
			segBucket = pendingBucket
			level = b.median
			pendingSet = false
		default:
			pendingBucket, pendingOff = bi, b.median
		}
	}
	ranges = append(ranges, matchRange{buckets[segBucket].from, len(pts)})
	bounds = append(bounds, segBucket)

	// refine each internal boundary to the match where the offset
	// actually switches, then cut the match list there
	idxBounds := make([]int, 0, len(ranges)+1)
	idxBounds = append(idxBounds, ranges[0].from)
	for i := 0; i+1 < len(ranges); i++ {
		idxBounds = append(idxBounds, switchPoint(pts, buckets[bounds[i]], buckets[bounds[i+1]], ranges[i], ranges[i+1], slope, jumpSplit))
	}
	idxBounds = append(idxBounds, len(pts))
	out := make([][]timedMatch, 0, len(ranges))
	for i := 0; i+1 < len(idxBounds); i++ {
		if idxBounds[i+1] > idxBounds[i] {
			out = append(out, pts[idxBounds[i]:idxBounds[i+1]:idxBounds[i+1]])
		}
	}
	return out
}

// switchPoint finds where consecutive matches switch from one offset
// level to the next around a segment boundary: it returns the index of
// the first match on the new level. The buckets only initialize the
// split (their median is a mixture at the cut); the flip point between
// the last match of one level and the first match of the next is
// frame-accurate because a cut is a hard edit.
func switchPoint(pts []timedMatch, tail, head matchBucket, a, b matchRange, slope, jumpSplit float64) int {
	const confirm = 2 // consecutive matches on the new level
	// the true switch sits anywhere from the start of the last bucket of
	// the old level to the end of the first bucket of the new one
	from := tail.from
	to := head.to
	if to-from < confirm+1 {
		return b.from
	}
	var levelA, levelB float64
	haveA, haveB := false, false
	if tail.from > a.from {
		levelA, haveA = medianOffset(pts[a.from:min(tail.from, a.to)], slope), true
	}
	if head.to < b.to {
		levelB, haveB = medianOffset(pts[max(head.to, b.from):b.to], slope), true
	}
	if !haveA || !haveB || levelA == levelB || math.Abs(levelB-levelA) <= jumpSplit {
		return b.from
	}
	run := 0
	for i := from; i < to; i++ {
		off := pts[i].ref - slope*pts[i].dub
		if math.Abs(off-levelB) < math.Abs(off-levelA) {
			run++
			if run >= confirm {
				return i - run + 1
			}
		} else {
			run = 0
		}
	}
	return b.from
}

// medianOffset is the median of ref − slope·dub over a match range.
func medianOffset(pts []timedMatch, slope float64) float64 {
	offs := make([]float64, len(pts))
	for i, p := range pts {
		offs[i] = p.ref - slope*p.dub
	}
	return medianFloat(offs)
}

// segLines turns match groups into dub-bounded lines for the constrained
// walks: each group gets its own intercept (shared speed) and spans dub
// time from its first match to the next group's first match.
func segLines(groups [][]timedMatch, speed, dubDur float64) []segLine {
	out := make([]segLine, 0, len(groups))
	for i, g := range groups {
		if len(g) == 0 {
			continue
		}
		s := segLine{
			line:     line{speed: speed, intercept: medianIntercept(g, speed)},
			DubStart: 0,
			DubEnd:   dubDur,
		}
		if i > 0 {
			s.DubStart = g[0].dub
		}
		if i+1 < len(groups) && len(groups[i+1]) > 0 {
			s.DubEnd = groups[i+1][0].dub
		}
		out = append(out, s)
	}
	return out
}

// segmentSlopes estimates the shared speed from pairs of matches inside
// the same segment, so no pair spans a mid-video cut. Slopes of all
// segments are pooled before the median, so big segments dominate.
func segmentSlopes(groups [][]timedMatch, interval float64, rng *rand.Rand) (float64, bool) {
	var slopes []float64
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		base, baseMax := pairBaseline(g, interval)
		n := len(g)
		if n <= exhaustivePairs {
			for i := 0; i < n; i++ {
				for j := i + 1; j < n; j++ {
					if s, ok := pairSlope(g[i], g[j], base, baseMax); ok {
						slopes = append(slopes, s)
					}
				}
			}
			continue
		}
		budget := max(1000, maxPairs/max(1, len(groups)))
		for k := 0; k < budget; k++ {
			i, j := rng.Intn(n), rng.Intn(n)
			if i == j {
				continue
			}
			if s, ok := pairSlope(g[i], g[j], base, baseMax); ok {
				slopes = append(slopes, s)
			}
		}
	}
	if len(slopes) < minPairs {
		return 0, false
	}
	return medianFloat(slopes), true
}

// theilSenSlope estimates ref-seconds per dub-second as the median slope
// over match pairs at least minBaseline apart. The baseline cap keeps
// pairs from spanning mid-video cuts often (a cut inflates such a pair
// by cut/baseline), and the median absorbs the few that do.
func theilSenSlope(pts []timedMatch, minBaseline, maxBaseline float64, rng *rand.Rand) (float64, bool) {
	n := len(pts)
	if n < 2 {
		return 0, false
	}
	var slopes []float64
	if n <= exhaustivePairs {
		slopes = make([]float64, 0, n*(n-1)/2)
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				if s, ok := pairSlope(pts[i], pts[j], minBaseline, maxBaseline); ok {
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
			if s, ok := pairSlope(pts[i], pts[j], minBaseline, maxBaseline); ok {
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
// baseline falls inside the window and the slope is physically
// plausible.
func pairSlope(a, b timedMatch, minBaseline, maxBaseline float64) (float64, bool) {
	dd := math.Abs(b.dub - a.dub)
	if dd < minBaseline || dd > maxBaseline {
		return 0, false
	}
	s := (b.ref - a.ref) / dd
	if s <= slopeMin || s >= slopeMax {
		return 0, false
	}
	return s, true
}

// pairBaseline picks the pair baseline window for slope estimation:
// min is a fraction of the match span (capped, never below a few
// sampling intervals so short files still fit) and max doubles it, so
// pairs stay short enough to be mostly cut-free. pts must be sorted by
// dub time.
func pairBaseline(pts []timedMatch, interval float64) (minBaseline, maxBaseline float64) {
	if len(pts) < 2 {
		return 0, 0
	}
	span := pts[len(pts)-1].dub - pts[0].dub
	if span <= 0 {
		return 0, 0
	}
	b := span / 10
	if b > baselineCap {
		b = baselineCap
	}
	b = max(b, 6*interval)
	return b, 2 * b
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

// buildMap trims each segment's matches against its line, turns the
// surviving groups into the final map, and computes the fit statistics.
// Reference spans run from the line-mapped dub boundaries: the head and
// tail follow the segment line to dub 0 / dub end (clamped to the file),
// and adjacent spans are clamped so a dub insert (offset jumping down)
// cannot make them overlap - the insert's dub audio is skipped, while a
// removed scene leaves a silent reference gap.
func buildMap(groups [][]timedMatch, speed, interval, refDur, dubDur float64, rng *rand.Rand) (*timingMap, error) {
	// per-segment robust trim: drop matches off the line, refit the
	// intercept, re-estimate the shared speed from same-segment pairs
	for iter := 0; iter < 3; iter++ {
		var kept [][]timedMatch
		for _, g := range groups {
			icpt := medianIntercept(g, speed)
			k := make([]timedMatch, 0, len(g))
			for _, p := range g {
				if math.Abs(p.ref-(icpt+speed*p.dub)) <= trimResidual*interval {
					k = append(k, p)
				}
			}
			if len(k) >= minSegmentMatches {
				kept = append(kept, k)
			}
		}
		if len(kept) == 0 {
			return nil, tooFewError(0)
		}
		groups = kept
		if s, ok := segmentSlopes(groups, interval, rng); ok {
			speed = s
		}
	}
	// merge adjacent segments whose intercepts agree: a spurious split
	// (bucket-level wobble) must not fragment the map or silence a
	// sliver of content, and a false split becomes truly harmless
	var merged [][]timedMatch
	for _, g := range groups {
		if n := len(merged); n > 0 && len(g) > 0 && len(merged[n-1]) > 0 &&
			math.Abs(medianIntercept(g, speed)-medianIntercept(merged[n-1], speed)) <= 0.05 {
			merged[n-1] = append(merged[n-1], g...)
			continue
		}
		merged = append(merged, g)
	}
	groups = merged
	if err := checkTilt(groups, speed, maxTilt); err != nil {
		return nil, err
	}

	m := &timingMap{Speed: speed}
	segs := make([]segment, 0, len(groups))
	prevRefEnd := 0.0
	prevDubEnd := 0.0
	var resid []float64
	for i, g := range groups {
		intercept := medianIntercept(g, speed)
		l := line{speed: speed, intercept: intercept}
		// dub bounds: the group's matches extended to the neighbouring
		// segments' territory (the last segment runs to the dub end)
		dubLo := 0.0
		if i > 0 {
			dubLo = g[0].dub
		}
		dubHi := dubDur
		if i+1 < len(groups) {
			dubHi = groups[i+1][0].dub
		}
		refLo := l.refAt(dubLo)
		refHi := l.refAt(dubHi)
		if i > 0 {
			// an insert shifts the offset down: never reach back into the
			// previous span
			refLo = math.Max(refLo, prevRefEnd)
		} else {
			refLo = math.Max(refLo, 0)
		}
		if i+1 < len(groups) {
			// the next segment's line may start earlier (insert): clamp
			// so the insert's dub audio is skipped
			nextLo := medianIntercept(groups[i+1], speed) + speed*groups[i+1][0].dub
			if nextLo < refHi {
				refHi = math.Max(nextLo, refLo)
			}
		} else {
			refHi = math.Min(refHi, refDur)
		}
		refLo = math.Min(math.Max(refLo, 0), refDur)
		refHi = math.Min(math.Max(refHi, 0), refDur)
		if refHi-refLo <= 0 {
			continue
		}
		dubStart := math.Max(prevDubEnd, l.dubAt(refLo))
		dubEnd := math.Min(math.Max(dubStart, l.dubAt(refHi)), dubDur)
		if dubEnd-dubStart <= 0 {
			continue
		}
		for _, p := range g {
			resid = append(resid, math.Abs(p.ref-l.refAt(p.dub)))
		}
		segs = append(segs, segment{
			DubStart: dubStart, DubEnd: dubEnd,
			RefStart: refLo, RefEnd: refHi,
			Slope: speed, Intercept: intercept,
			Matches:  len(g),
			Residual: medianFloat(resid[len(resid)-len(g):]),
		})
		prevRefEnd = refHi
		prevDubEnd = dubEnd
	}
	if len(segs) == 0 {
		return nil, tooFewError(0)
	}
	m.Segments = segs
	m.MedianResid = medianFloat(resid)
	m.P95Resid = percentile(resid, 0.95)
	for _, s := range segs {
		m.NInliers += s.Matches
		m.SpanSupport += s.RefEnd - s.RefStart
		m.Overlap += s.DubEnd - s.DubStart
		m.NFine += int((s.DubEnd - s.DubStart) / interval)
	}
	if m.NFine > 0 {
		m.InlierFrac = float64(m.NInliers) / float64(m.NFine)
	}
	// span support: how much of the reference the dub could cover (per
	// the fitted speed and offsets) the segments actually cover - a
	// modified intro or credits legitimately leaves the ends out, but a
	// fit matching only a localized cluster cannot hide here
	extentLo := math.Max(0, segs[0].Intercept)
	extentHi := math.Min(refDur, segs[len(segs)-1].Intercept+speed*dubDur)
	if extent := extentHi - extentLo; extent > 0 {
		m.SpanSupport = math.Min(1, (m.SpanSupport)/extent)
	} else {
		m.SpanSupport = 0
	}
	return m, nil
}

// refAtDub maps dubbed time to reference time through the covering
// segment, reporting whether the time is covered.
func (m *timingMap) refAtDub(dub float64) (float64, bool) {
	i := sort.Search(len(m.Segments), func(i int) bool {
		return m.Segments[i].DubEnd > dub
	})
	if i >= len(m.Segments) || dub < m.Segments[i].DubStart {
		return 0, false
	}
	return m.Segments[i].refAt(dub), true
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

// tooFewError is the refusal for content that does not establish a map.
func tooFewError(n int) error {
	return fmt.Errorf("too few reliable matches (%d) to establish one timing map - the files likely are not the same edit", n)
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
