package align

import (
	"fmt"
	"math"
	"sort"

	"github.com/hranicka/mediatool/internal/vhash"
)

// frameMatch is one dubbed frame's best reference match: the dubbed frame
// index, the reference frame index (-1 when no match was accepted) and the
// hash distance.
type frameMatch struct {
	dub, ref, dist int
}

const (
	// minSegmentPoints is the number of reliable matches a continuous run
	// needs to become a timing segment (shorter runs are match noise).
	minSegmentPoints = 3
	// localConfirm is the number of consecutive frames a local match must
	// agree with. A lone false hit near a cut or a similar shot is rejected
	// instead of bending the map.
	localConfirm = 3
	// breakOffset is the offset jump (seconds) that splits a run. The match
	// grid resolves about one frame, so a smaller jump is fit noise.
	breakOffset = 0.5
	// maxGap is the largest gap (seconds) between reliable matches that
	// still counts as continuous; longer gaps are split and re-merged only
	// when the offsets on both sides agree.
	maxGap = 2.0
	// mergeOffset is how closely two runs' observed offsets must agree at
	// the boundary to be treated as one continuous segment.
	mergeOffset = 0.3
	// outlierOffset is the residual (seconds) above which a match inside a
	// run is dropped as an outlier.
	outlierOffset = 3.0
	// minCoverage is the fraction of each file the map must cover for the
	// result to be trusted. Half a file is already a different edit, not a
	// few skipped scenes.
	minCoverage = 0.8
	// bridgeGap is the longest reference gap (seconds) that is bridged
	// when the fitted offsets on both sides agree: a lost match stretch,
	// not an edit.
	bridgeGap = 60.0
	// bridgeOffset is the offset difference (seconds) allowed across a
	// bridged gap. A real removed scene shifts the offset by its length.
	bridgeOffset = 0.75
	// globalResidual is the largest median residual (seconds) a single
	// global fit may have to replace a fragmented piecewise map.
	globalResidual = 0.75
	// minRemainingRatio is the smallest reference/dub duration ratio that a
	// resync candidate may leave. A jump that leaves far less reference than
	// the dub still to play is a repeated late shot, not the continuation.
	minRemainingRatio = 0.25
	// slope bounds for a plausible timing segment. Real edits land at
	// 1.0 (same speed) and 1.043 (PAL speedup).
	slopeMin = 0.5
	slopeMax = 2.0
)

// segment is one continuous run mapping dubbed time to reference time:
// ref = intercept + slope*dub. RefStart/RefEnd are the covered reference
// span and DubStart/DubEnd the corresponding dubbed span.
type segment struct {
	DubStart, DubEnd float64
	RefStart, RefEnd float64
	Slope            float64
	Intercept        float64
	Matches          int
	Residual         float64 // median absolute match residual in seconds
}

// refAt maps dubbed time to reference time on the segment's line.
func (s segment) refAt(dub float64) float64 {
	return s.Intercept + s.Slope*dub
}

// dubAt maps reference time back to dubbed time on the segment's line.
func (s segment) dubAt(ref float64) float64 {
	return (ref - s.Intercept) / s.Slope
}

// timingMap is the segmented map from dubbed to reference time plus the
// match statistics surfaced in the run report. A reference region not
// covered by any segment has no dub counterpart (silence on the dub track).
type timingMap struct {
	Segments    []segment
	NRel, NOut  int
	MatchedRef  float64
	MatchedDub  float64
	RefCoverage float64
	DubCoverage float64
	Fallback    bool // fragmented map replaced by one global fit
}

// timedMatch is one reliable frame match in seconds.
type timedMatch struct {
	dub, ref float64
}

// offsetModel fits ref-dub as a function of dub time: offset = intercept +
// slope*dub.
type offsetModel struct {
	slope, intercept float64
}

func (m offsetModel) at(dub float64) float64 {
	return m.intercept + m.slope*dub
}

// offsetAccum carries the least-squares sums of a growing match run, so
// splitRuns can evaluate the current model in constant time per match.
type offsetAccum struct {
	n, sx, sy, sxx, sxy float64
}

func (a *offsetAccum) add(p timedMatch) {
	x, y := p.dub, p.ref-p.dub
	a.n++
	a.sx += x
	a.sy += y
	a.sxx += x * x
	a.sxy += x * y
}

func (a offsetAccum) model() offsetModel {
	if a.n == 0 {
		return offsetModel{}
	}
	den := a.sxx - a.sx*a.sx/a.n
	if math.Abs(den) < 1e-9 {
		return offsetModel{intercept: a.sy / a.n}
	}
	slope := (a.sxy - a.sx*a.sy/a.n) / den
	return offsetModel{slope: slope, intercept: a.sy/a.n - slope*a.sx/a.n}
}

// fitOffset fits the offset line by least squares.
func fitOffset(points []timedMatch) offsetModel {
	var a offsetAccum
	for _, p := range points {
		a.add(p)
	}
	return a.model()
}

// fitIntercept fits a line with a fixed slope by the median offset, which
// is not dragged around by a few bad frame matches.
func fitIntercept(points []timedMatch, slope float64) (intercept, residual float64) {
	if len(points) == 0 {
		return 0, 0
	}
	vals := make([]float64, len(points))
	for i, p := range points {
		vals[i] = p.ref - slope*p.dub
	}
	intercept = medianFloat(vals)
	for i := range vals {
		vals[i] = math.Abs(vals[i] - intercept)
	}
	return intercept, medianFloat(vals)
}

// speedEstimate returns a robust global speed ratio (reference seconds per
// dubbed second) when there is enough data. Frame matches are bucketed into
// windows, each bucket is reduced to its median offset, and the median of
// the slopes between consecutive buckets is taken. An isolated offset jump
// from a scene edit affects one bucket pair, not the median. ok is false
// for short files, where per-run fitting is more reliable.
func speedEstimate(rel []timedMatch) (float64, bool) {
	const window = 20.0
	type bucket struct {
		dub, offset float64
	}
	var buckets []bucket
	var cur []float64
	curID := -1
	flush := func() {
		if len(cur) >= 5 {
			buckets = append(buckets, bucket{dub: (float64(curID) + 0.5) * window, offset: medianFloat(cur)})
		}
		cur = cur[:0]
	}
	for _, p := range rel {
		id := int(p.dub / window)
		if curID >= 0 && id != curID {
			flush()
		}
		curID = id
		cur = append(cur, p.ref-p.dub)
	}
	flush()
	if len(buckets) < 3 {
		return 0, false
	}
	slopes := make([]float64, 0, len(buckets)-1)
	for i := 1; i < len(buckets); i++ {
		dt := buckets[i].dub - buckets[i-1].dub
		if dt <= 0 {
			continue
		}
		s := 1 + (buckets[i].offset-buckets[i-1].offset)/dt
		if s >= slopeMin && s <= slopeMax {
			slopes = append(slopes, s)
		}
	}
	if len(slopes) < 5 {
		return 0, false
	}
	return medianFloat(slopes), true
}

// centerOffset shifts a fitted line so the median residual is zero. Unlike
// the least-squares mean it is not dragged around by a few bad matches.
func centerOffset(points []timedMatch, model offsetModel) offsetModel {
	if len(points) == 0 {
		return model
	}
	res := make([]float64, len(points))
	for i, p := range points {
		res[i] = (p.ref - p.dub) - model.at(p.dub)
	}
	model.intercept += medianFloat(res)
	return model
}

// medianResidual returns the median absolute residual of the matches to
// the model - a robust estimate of the map's timing precision.
func medianResidual(points []timedMatch, model offsetModel) float64 {
	if len(points) == 0 {
		return 0
	}
	res := make([]float64, len(points))
	for i, p := range points {
		res[i] = math.Abs((p.ref - p.dub) - model.at(p.dub))
	}
	return medianFloat(res)
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

// findMatches pairs each dubbed frame with its best-matching reference
// frame. The walk starts with a local forward search around the previous
// match (only a couple of frames of backward slack: the reference position
// is monotonic); when the local search loses track (a long intro, a skipped
// or an extra scene), it periodically searches the whole remaining
// reference and accepts a jump only after several consecutive frames
// confirm it.
func findMatches(ref, dub [][vhash.HashLen]byte, window, matchDist int, interval float64) []frameMatch {
	matches := make([]frameMatch, 0, len(dub))
	prev := 0
	lost := 0
	resyncEvery := max(1, int(math.Ceil(1.0/interval)))
	for j := range dub {
		// the reference position is monotonic: only jitter may step back a
		// couple of frames, never a repeated scene
		lo := max(0, prev-2)
		hi := min(len(ref), prev+window+1)
		best, dist := vhash.WindowBest(dub, ref, j, lo, hi, prev+1)
		if dist < matchDist && confirmMatch(ref, dub, j, best, matchDist, localConfirm) {
			matches = append(matches, frameMatch{dub: j, ref: best, dist: dist})
			prev = best
			lost = 0
			continue
		}
		matches = append(matches, frameMatch{dub: j, ref: -1, dist: dist})
		lost++
		// The first few lost frames retry every time: a real jump usually
		// becomes matchable a frame or two later, and waiting for the coarse
		// cadence would leave a second of silence. After that, scan less
		// often so unrelated stretches stay cheap.
		if lost <= 3 || lost%resyncEvery == 0 {
			if r, d, ok := resyncMatch(ref, dub, j, prev, matchDist, interval); ok {
				matches[len(matches)-1] = frameMatch{dub: j, ref: r, dist: d}
				prev = r
				lost = 0
			}
		}
	}
	return matches
}

// resyncMatch searches the reference from the previous position onward
// for the dubbed frame at j and verifies candidates against the next
// frames. The scan runs at every sampling position: perceptual hashes are
// unique per frame, so a strided scan usually misses the true match and
// reattaches seconds late. The first confirmed candidate wins - the
// earliest position is the natural continuation, and a repeated later
// scene must not pull the walk forward. Requiring a confirmation run keeps
// repeated intros and static scenes from causing false jumps.
func resyncMatch(ref, dub [][vhash.HashLen]byte, j, prev, matchDist int, interval float64) (int, int, bool) {
	if prev >= len(ref) || j >= len(dub) {
		return 0, 0, false
	}
	confirm := max(3, int(math.Ceil(1.0/interval)))
	remainingDub := float64(len(dub)-j) * interval
	for k := prev; k < len(ref); k++ {
		if float64(len(ref)-k)*interval < minRemainingRatio*remainingDub {
			break
		}
		d := vhash.Hamming(dub[j], ref[k])
		if d >= matchDist {
			continue
		}
		if confirmMatch(ref, dub, j, k, matchDist, confirm) {
			return k, d, true
		}
	}
	return 0, 0, false
}

// confirmMatch reports whether ref[r:] continues to match dub[j:] for the
// requested number of frames, allowing one frame of slack for framerate
// drift. A short confirmation is enough to follow a continuous run; a jump
// needs the longer run so a repeated shot cannot hijack the walk.
func confirmMatch(ref, dub [][vhash.HashLen]byte, j, r, matchDist, confirm int) bool {
	want := min(confirm, len(dub)-j)
	ok := 0
	for k := 0; k < want && r+k < len(ref); k++ {
		bestD := vhash.Hamming(dub[j+k], ref[r+k])
		if r+k > 0 {
			bestD = min(bestD, vhash.Hamming(dub[j+k], ref[r+k-1]))
		}
		if r+k+1 < len(ref) {
			bestD = min(bestD, vhash.Hamming(dub[j+k], ref[r+k+1]))
		}
		if bestD >= matchDist {
			break
		}
		ok++
	}
	return ok == want
}

// buildTimingMap turns reliable frame matches into a piecewise (segmented)
// monotonic timing map. Continuous runs are fitted with an affine offset
// line; runs split by an offset jump, a dub-only insert or a reference-only
// gap stay separate segments, and runs whose lines agree at the boundary
// are merged again. This tolerates shifted starts/ends, framerate
// differences and skipped or extra scenes in one pass.
func buildTimingMap(matches []frameMatch, interval, refDur, dubDur float64, matchDist int) (*timingMap, error) {
	var rel []timedMatch
	for _, m := range matches {
		if m.ref < 0 || m.dist >= matchDist {
			continue
		}
		rel = append(rel, timedMatch{
			dub: float64(m.dub) * interval,
			ref: float64(m.ref) * interval,
		})
	}
	nRel := len(rel)
	if nRel < 10 {
		return nil, fmt.Errorf("too few reliable matches (%d) to build a timing map", nRel)
	}

	runs := splitRuns(rel)
	slope, shared := speedEstimate(rel)
	if !shared {
		slope = 0 // per-run fit
	}
	var pruned [][]timedMatch
	for _, run := range runs {
		points, _ := pruneRun(run, slope)
		if len(points) >= minSegmentPoints {
			pruned = append(pruned, points)
		}
	}
	pruned = mergeRuns(pruned, slope)

	var segs []segment
	for _, run := range pruned {
		points, _ := pruneRun(run, slope)
		if len(points) < minSegmentPoints {
			continue
		}
		s, ok := runSegment(points, slope, interval, refDur, dubDur)
		if !ok {
			continue
		}
		segs = append(segs, s)
	}
	segs = nonOverlapping(segs)
	segs = bridgeGaps(segs, refDur, dubDur)
	segs = normalizeJoins(segs, dubDur)
	fallback := false
	if gs, ok := globalFallback(rel, segs, slope, interval, refDur, dubDur); ok {
		segs = []segment{gs}
		fallback = true
	}

	kept := 0
	for _, s := range segs {
		kept += s.Matches
	}

	tm := &timingMap{Segments: segs, NRel: nRel, NOut: nRel - kept, Fallback: fallback}
	for _, s := range segs {
		tm.MatchedRef += s.RefEnd - s.RefStart
		tm.MatchedDub += s.DubEnd - s.DubStart
	}
	if refDur > 0 {
		tm.RefCoverage = math.Min(1, tm.MatchedRef/refDur)
	}
	if dubDur > 0 {
		tm.DubCoverage = math.Min(1, tm.MatchedDub/dubDur)
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("too few reliable matches (%d) to build a timing map", nRel)
	}
	return tm, nil
}

// splitRuns groups reliable matches into offset-continuous runs.
func splitRuns(rel []timedMatch) [][]timedMatch {
	var (
		runs [][]timedMatch
		cur  []timedMatch
		acc  offsetAccum
	)
	for _, m := range rel {
		if len(cur) == 0 {
			cur = append(cur, m)
			acc.add(m)
			continue
		}
		prev := cur[len(cur)-1]
		gap := m.dub - prev.dub
		model := acc.model()
		resid := (m.ref - m.dub) - model.at(m.dub)
		boundary := false
		if len(cur) >= 2 {
			boundary = math.Abs(resid) > breakOffset || gap > maxGap
		} else {
			boundary = gap <= maxGap && math.Abs((m.ref-m.dub)-(prev.ref-prev.dub)) > breakOffset
		}
		if boundary {
			runs = append(runs, cur)
			cur = []timedMatch{m}
			acc = offsetAccum{}
			acc.add(m)
			continue
		}
		cur = append(cur, m)
		acc.add(m)
	}
	if len(cur) > 0 {
		runs = append(runs, cur)
	}
	return runs
}

// pruneRun drops matches far off the run's offset line. A positive slope
// is the shared global speed; zero fits each run's own line.
func pruneRun(points []timedMatch, slope float64) ([]timedMatch, int) {
	original := len(points)
	for iter := 0; iter < 3 && len(points) >= minSegmentPoints; iter++ {
		runSlope := slope
		var intercept float64
		if runSlope > 0 {
			intercept, _ = fitIntercept(points, runSlope)
		} else {
			model := fitOffset(points)
			intercept = model.intercept
			runSlope = 1 + model.slope
		}
		if !isFinite(intercept) || !isFinite(runSlope) {
			break
		}
		kept := make([]timedMatch, 0, len(points))
		for _, p := range points {
			if math.Abs(p.ref-(intercept+runSlope*p.dub)) <= outlierOffset {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(points) {
			break
		}
		points = kept
	}
	return points, original - len(points)
}

// mergeRuns joins adjacent runs whose observed offset agrees at the
// boundary; this restores continuity across unreliable stretches and
// reconnects the runs around a single outlier. The offset is ref minus the
// shared slope times dub, so a global speed difference does not look like
// a scene jump.
func mergeRuns(runs [][]timedMatch, slope float64) [][]timedMatch {
	for changed := true; changed; {
		changed = false
		for i := 0; i+1 < len(runs); i++ {
			a, b := runs[i], runs[i+1]
			if len(a) == 0 || len(b) == 0 {
				continue
			}
			if math.Abs(edgeOffset(a, slope, true)-edgeOffset(b, slope, false)) <= mergeOffset {
				runs[i] = append(a, b...)
				runs = append(runs[:i+1], runs[i+2:]...)
				changed = true
				break
			}
		}
	}
	return runs
}

// edgeOffset is the median offset of the first or last few matches of a
// run at the shared slope - a stable local level even when the run is
// short and its own slope estimate would be noisy.
func edgeOffset(points []timedMatch, slope float64, tail bool) float64 {
	n := min(5, len(points))
	if n == 0 {
		return 0
	}
	if slope <= 0 {
		slope = 1
	}
	vals := make([]float64, n)
	for i := 0; i < n; i++ {
		j := i
		if tail {
			j = len(points) - n + i
		}
		vals[i] = points[j].ref - slope*points[j].dub
	}
	return medianFloat(vals)
}

// runSegment fits the offset of one continuous run at the shared slope
// (or its own fit when slope is zero) and bounds it to the run's matches.
func runSegment(points []timedMatch, slope, interval, refDur, dubDur float64) (segment, bool) {
	var intercept, residual float64
	if slope > 0 {
		if !isFinite(slope) || slope < slopeMin || slope > slopeMax {
			return segment{}, false
		}
		intercept, residual = fitIntercept(points, slope)
	} else {
		model := centerOffset(points, fitOffset(points))
		slope = 1 + model.slope
		intercept = model.intercept
		residual = medianResidual(points, model)
		if !isFinite(slope) || slope < slopeMin || slope > slopeMax {
			return segment{}, false
		}
	}
	if !isFinite(intercept) {
		return segment{}, false
	}
	dubStart := points[0].dub - interval/2
	if points[0].dub <= interval {
		dubStart = 0
	}
	dubEnd := points[len(points)-1].dub + interval/2
	if dubDur-points[len(points)-1].dub <= interval {
		dubEnd = dubDur
	}
	dubStart = max(0, dubStart)
	dubEnd = min(dubDur, dubEnd)
	if dubEnd <= dubStart {
		return segment{}, false
	}
	refStart := intercept + slope*dubStart
	refEnd := intercept + slope*dubEnd
	refStart = min(max(refStart, 0), refDur)
	refEnd = min(max(refEnd, 0), refDur)
	if refEnd-refStart <= 0 {
		return segment{}, false
	}
	return segment{
		DubStart:  dubStart,
		DubEnd:    dubEnd,
		RefStart:  refStart,
		RefEnd:    refEnd,
		Slope:     slope,
		Intercept: intercept,
		Matches:   len(points),
		Residual:  residual,
	}, true
}

// nonOverlapping sorts segments by dubbed time, trims boundary overlaps
// with the previous segment and drops a segment that mostly conflicts.
func nonOverlapping(segs []segment) []segment {
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].DubStart != segs[j].DubStart {
			return segs[i].DubStart < segs[j].DubStart
		}
		return segs[i].RefStart < segs[j].RefStart
	})
	out := segs[:0]
	for _, s := range segs {
		if len(out) > 0 {
			prev := out[len(out)-1]
			if s.DubStart < prev.DubEnd {
				if prev.DubEnd-s.DubStart > clashLimit(s.DubEnd-s.DubStart) {
					continue
				}
				s.DubStart = prev.DubEnd
			}
			if s.RefStart < prev.RefEnd {
				if prev.RefEnd-s.RefStart > clashLimit(s.RefEnd-s.RefStart) {
					continue
				}
				s.RefStart = prev.RefEnd
				s.DubStart = max(s.DubStart, s.dubAt(s.RefStart))
			}
			if s.RefEnd-s.RefStart <= 0 || s.DubEnd <= s.DubStart {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// clashLimit is how much of a segment may overlap the previous one and
// still be treated as boundary noise. A conflict that eats more than a
// quarter of the segment is a different map, not a rounding artifact.
func clashLimit(span float64) float64 {
	return max(0.5, 0.25*span)
}

// bridgeGaps covers small reference gaps whose offset is continuous on both
// sides: those are lost matches (fades, noise, a static shot), not edits, so
// extending the preceding line keeps the dub audible. A real removed scene
// shifts the offset by its length and stays silent.
func bridgeGaps(segs []segment, refDur, dubDur float64) []segment {
	if len(segs) == 0 {
		return segs
	}
	if first := &segs[0]; first.RefStart > 0 && first.RefStart <= bridgeGap {
		if d := first.dubAt(0); d >= 0 && d < first.DubStart {
			first.RefStart = 0
			first.DubStart = d
		}
	}
	for i := 1; i < len(segs); i++ {
		a, b := &segs[i-1], &segs[i]
		gap := b.RefStart - a.RefEnd
		if gap <= 0 || gap > bridgeGap {
			continue
		}
		if math.Abs((b.RefStart-b.Slope*b.DubStart)-(a.RefEnd-a.Slope*a.DubEnd)) > bridgeOffset {
			continue
		}
		d := a.dubAt(b.RefStart)
		if d <= a.DubEnd || d > dubDur {
			continue
		}
		a.RefEnd = b.RefStart
		a.DubEnd = d
	}
	if last := &segs[len(segs)-1]; last.RefEnd < refDur && refDur-last.RefEnd <= bridgeGap {
		if d := last.dubAt(refDur); d > last.DubEnd && d <= dubDur {
			last.RefEnd = refDur
			last.DubEnd = d
		}
	}
	return segs
}

// normalizeJoins makes the map monotonic: a segment's first mapped dub
// position may not fall behind the previous segment's last read position.
// Such a rewind is fit noise (a real edit advances the dub), and the
// forward-only resampler would write silence until it caught up. Shifting
// the intercept keeps the covered reference span intact, unlike trimming
// the start, which would leave that span silent.
func normalizeJoins(segs []segment, dubDur float64) []segment {
	out := segs[:0]
	for _, s := range segs {
		if len(out) > 0 {
			prev := &out[len(out)-1]
			mapped := s.dubAt(s.RefStart)
			if mapped < prev.DubEnd {
				s.Intercept = s.RefStart - s.Slope*prev.DubEnd
				mapped = prev.DubEnd
			}
			s.DubStart = mapped
			s.DubEnd = s.dubAt(s.RefEnd)
			if s.DubEnd > dubDur {
				s.DubEnd = dubDur
				s.RefEnd = s.refAt(dubDur)
			}
			if s.RefEnd <= s.RefStart || s.DubEnd <= s.DubStart {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// globalFallback fits one line at the shared speed through all reliable
// matches when the piecewise map is fragmented or incomplete. It only
// replaces the map when the global line explains the whole reference
// almost to both ends and is precise: genuine edits shift the offset and
// fail both tests.
func globalFallback(rel []timedMatch, segs []segment, slope, interval, refDur, dubDur float64) (segment, bool) {
	if len(segs) <= 2 || refDur <= 0 {
		return segment{}, false
	}
	covered := 0.0
	for _, s := range segs {
		covered += s.RefEnd - s.RefStart
	}
	if len(segs) <= 8 && covered >= 0.9*refDur {
		return segment{}, false
	}
	points, _ := pruneRun(rel, slope)
	if len(points) < minSegmentPoints {
		return segment{}, false
	}
	s, ok := runSegment(points, slope, interval, refDur, dubDur)
	if !ok || s.Residual > globalResidual {
		return segment{}, false
	}
	if s.RefStart > 1 || s.RefEnd < refDur-1 {
		return segment{}, false
	}
	if s.RefEnd-s.RefStart < covered-0.5 {
		return segment{}, false
	}
	return s, true
}

// checkCoverage refuses maps that only explain a small part of either
// file: that is the signature of unrelated content or matching noise.
func (tm *timingMap) checkCoverage() error {
	if tm.RefCoverage < minCoverage || tm.DubCoverage < minCoverage {
		return fmt.Errorf("timing map covers only %.0f%% of the reference and %.0f%% of the dub (need at least %.0f%% each) - the files likely are not the same edit",
			tm.RefCoverage*100, tm.DubCoverage*100, minCoverage*100)
	}
	return nil
}

// refAtDub maps dubbed time to reference time, reporting whether the time
// is covered by a segment.
func (tm *timingMap) refAtDub(dub float64) (float64, bool) {
	i := sort.Search(len(tm.Segments), func(i int) bool {
		return tm.Segments[i].DubEnd > dub
	})
	if i >= len(tm.Segments) || dub < tm.Segments[i].DubStart {
		return 0, false
	}
	return tm.Segments[i].refAt(dub), true
}

// isFinite reports whether v is a usable number.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
