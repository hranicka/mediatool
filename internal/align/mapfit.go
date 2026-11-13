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
	// minSegmentDuration drops segments too short to carry useful audio.
	minSegmentDuration = 1.0
	// breakOffset is the offset jump (seconds) that splits a run.
	breakOffset = 1.0
	// maxGap is the largest gap (seconds) between reliable matches that
	// still counts as continuous; longer gaps are split and re-merged only
	// when the fitted maps agree at the boundary.
	maxGap = 2.0
	// mergeOffset is how closely two runs' fitted maps must agree at the
	// boundary to be treated as one continuous segment.
	mergeOffset = 1.0
	// outlierOffset is the residual (seconds) above which a match inside a
	// run is dropped as an outlier.
	outlierOffset = 3.0
	// minCoverage is the fraction of each file the map must cover for the
	// result to be trusted.
	minCoverage = 0.5
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

// fitOffset fits the offset line by least squares.
func fitOffset(points []timedMatch) offsetModel {
	if len(points) == 0 {
		return offsetModel{}
	}
	if len(points) == 1 {
		return offsetModel{intercept: points[0].ref - points[0].dub}
	}
	var sx, sy, sxx, sxy float64
	for _, p := range points {
		x := p.dub
		y := p.ref - p.dub
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(len(points))
	den := sxx - sx*sx/n
	if math.Abs(den) < 1e-9 {
		return offsetModel{intercept: sy / n}
	}
	slope := (sxy - sx*sy/n) / den
	return offsetModel{slope: slope, intercept: sy/n - slope*sx/n}
}

// findMatches pairs each dubbed frame with its best-matching reference
// frame. The walk starts with a local ±window search around the previous
// match; when the local search loses track (a long intro, a skipped or an
// extra scene), it periodically searches the whole remaining reference
// and accepts a jump only after several consecutive frames confirm it.
func findMatches(ref, dub [][vhash.HashLen]byte, window, matchDist int, interval float64) []frameMatch {
	matches := make([]frameMatch, 0, len(dub))
	prev := 0
	lost := 0
	resyncEvery := max(1, int(math.Ceil(1.0/interval)))
	for j := range dub {
		lo := max(0, prev-window)
		hi := min(len(ref), prev+window+1)
		best, dist := vhash.WindowBest(dub, ref, j, lo, hi, prev+1)
		if dist < matchDist {
			matches = append(matches, frameMatch{dub: j, ref: best, dist: dist})
			prev = best
			lost = 0
			continue
		}
		matches = append(matches, frameMatch{dub: j, ref: -1, dist: dist})
		lost++
		if lost == 1 || lost%resyncEvery == 0 {
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
// for the dubbed frame at j and verifies the candidate against the next
// frames. Requiring a confirmation run keeps repeated intros and static
// scenes from causing false jumps.
func resyncMatch(ref, dub [][vhash.HashLen]byte, j, prev, matchDist int, interval float64) (int, int, bool) {
	if prev >= len(ref) || j >= len(dub) {
		return 0, 0, false
	}
	stride := max(1, int(math.Round(0.8/interval)))
	best, bestDist := coarseBest(ref, dub[j], prev, len(ref), stride, prev)
	if best < 0 || bestDist >= matchDist {
		return 0, 0, false
	}
	// refine around the coarse hit
	lo := max(prev, best-stride)
	hi := min(len(ref), best+stride+1)
	r, d := vhash.WindowBest(dub, ref, j, lo, hi, best)
	if d >= matchDist {
		return 0, 0, false
	}

	confirm := max(3, int(math.Ceil(1.0/interval)))
	ok := 0
	for k := 0; k < confirm && j+k < len(dub) && r+k < len(ref); k++ {
		// one frame of slack absorbs framerate drift inside the window
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
	if ok < min(3, len(dub)-j) {
		return 0, 0, false
	}
	return r, d, true
}

// coarseBest scans b[lo:hi) with a stride and refines the best hit.
func coarseBest(b [][vhash.HashLen]byte, h [vhash.HashLen]byte, lo, hi, stride, want int) (int, int) {
	best, bestDist := -1, vhash.Side*vhash.Side+1
	for k := lo; k < hi; k += stride {
		if d := vhash.Hamming(h, b[k]); d < bestDist ||
			(d == bestDist && (best < 0 || absInt(k-want) < absInt(best-want))) {
			best, bestDist = k, d
		}
	}
	if best < 0 {
		return -1, bestDist
	}
	rlo, rhi := max(lo, best-stride), min(hi, best+stride+1)
	for k := rlo; k < rhi; k++ {
		if d := vhash.Hamming(h, b[k]); d < bestDist ||
			(d == bestDist && absInt(k-want) < absInt(best-want)) {
			best, bestDist = k, d
		}
	}
	return best, bestDist
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
	var pruned [][]timedMatch
	for _, run := range runs {
		points, _ := pruneRun(run)
		if len(points) < minSegmentPoints {
			continue
		}
		if points[len(points)-1].dub-points[0].dub < minSegmentDuration {
			continue
		}
		pruned = append(pruned, points)
	}
	pruned = mergeRuns(pruned)

	var segs []segment
	for _, run := range pruned {
		points, _ := pruneRun(run)
		if len(points) < minSegmentPoints {
			continue
		}
		s, ok := runSegment(points, interval, refDur, dubDur)
		if !ok {
			continue
		}
		segs = append(segs, s)
	}
	segs = nonOverlapping(segs)
	kept := 0
	for _, s := range segs {
		kept += s.Matches
	}

	tm := &timingMap{Segments: segs, NRel: nRel, NOut: nRel - kept}
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
	var runs [][]timedMatch
	var cur []timedMatch
	for _, m := range rel {
		if len(cur) == 0 {
			cur = append(cur, m)
			continue
		}
		prev := cur[len(cur)-1]
		gap := m.dub - prev.dub
		model := fitOffset(cur)
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
			continue
		}
		cur = append(cur, m)
	}
	if len(cur) > 0 {
		runs = append(runs, cur)
	}
	return runs
}

// pruneRun drops matches far off the run's fitted offset line.
func pruneRun(points []timedMatch) ([]timedMatch, int) {
	original := len(points)
	for iter := 0; iter < 3 && len(points) >= minSegmentPoints; iter++ {
		model := fitOffset(points)
		kept := make([]timedMatch, 0, len(points))
		for _, p := range points {
			if math.Abs((p.ref-p.dub)-model.at(p.dub)) <= outlierOffset {
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

// mergeRuns joins adjacent runs whose fitted maps agree at the boundary;
// this restores continuity across unreliable stretches and reconnects the
// runs around a single outlier.
func mergeRuns(runs [][]timedMatch) [][]timedMatch {
	for changed := true; changed; {
		changed = false
		for i := 0; i+1 < len(runs); i++ {
			a, b := runs[i], runs[i+1]
			if len(a) == 0 || len(b) == 0 {
				continue
			}
			ma, mb := fitOffset(a), fitOffset(b)
			boundary := b[0].dub
			if math.Abs(ma.at(boundary)-mb.at(boundary)) <= mergeOffset {
				runs[i] = append(a, b...)
				runs = append(runs[:i+1], runs[i+2:]...)
				changed = true
				break
			}
		}
	}
	return runs
}

// runSegment fits and bounds one continuous run.
func runSegment(points []timedMatch, interval, refDur, dubDur float64) (segment, bool) {
	model := fitOffset(points)
	slope := 1 + model.slope
	if !isFinite(model.intercept) || !isFinite(slope) || slope < slopeMin || slope > slopeMax {
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
	refStart := model.intercept + slope*dubStart
	refEnd := model.intercept + slope*dubEnd
	refStart = min(max(refStart, 0), refDur)
	refEnd = min(max(refEnd, 0), refDur)
	if refEnd-refStart < minSegmentDuration {
		return segment{}, false
	}
	return segment{
		DubStart:  dubStart,
		DubEnd:    dubEnd,
		RefStart:  refStart,
		RefEnd:    refEnd,
		Slope:     slope,
		Intercept: model.intercept,
		Matches:   len(points),
	}, true
}

// nonOverlapping sorts segments by dubbed time and drops or trims the ones
// whose reference span collides with the previous segment (a sign of
// conflicting matches).
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
				if prev.DubEnd-s.DubStart > 0.5 {
					continue
				}
				s.DubStart = prev.DubEnd
			}
			if s.RefStart < prev.RefEnd {
				if prev.RefEnd-s.RefStart > 0.5 {
					continue
				}
				s.RefStart = prev.RefEnd
				s.DubStart = max(s.DubStart, s.dubAt(s.RefStart))
			}
			if s.RefEnd-s.RefStart < minSegmentDuration || s.DubEnd <= s.DubStart {
				continue
			}
		}
		out = append(out, s)
	}
	return out
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
