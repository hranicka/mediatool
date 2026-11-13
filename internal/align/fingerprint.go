package align

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

const (
	// fingerprintKey is the pseudo sampling interval under which a content
	// fingerprint is stored in the frame cache. No video is sampled at this
	// interval; it only keeps fingerprints apart from full timing hashes.
	fingerprintKey = -1.0
	// fingerprintWindows is the number of short samples taken across a file
	// for pairing. Spreading them over the whole duration keeps the
	// fingerprint independent of a possible intro, credits or a cold open.
	fingerprintWindows = 12
	// fingerprintWindow is the sampled length (seconds) of one window.
	fingerprintWindow = 4.0
	// fingerprintFPS is the sampling rate inside a fingerprint window.
	fingerprintFPS = 2
	// fingerprintDist is the Hamming distance (of 1024 bits) below which a
	// sampled frame counts as the same shot across two releases.
	fingerprintDist = 60
	// minFingerprintHits is the number of matching sampled frames that make
	// a content pairing credible. With 12 x 4 s x 2 fps a file yields about
	// 96 fingerprints, so this is roughly 3 % of the samples: enough for a
	// single clear peak while unrelated episodes stay at zero.
	minFingerprintHits = 3
	// fingerprintMargin is how many hits the best candidate must lead the
	// runner-up by, so a few frames from a shared intro or a similar shot
	// cannot win a pairing.
	fingerprintMargin = 2
)

// fingerprint is a sparse content signature of a video: short frame samples
// spread across the file. It is much cheaper than extracting timing hashes
// and only answers "is this the same episode", not "where".
type fingerprint struct {
	hashes [][vhash.HashLen]byte
}

// contentMatch scores one reference/dub fingerprint pair. hits is the number
// of dub samples that have a close frame anywhere in the reference; score
// normalizes it by the sample count so files with slightly different
// fingerprint lengths stay comparable.
type contentMatch struct {
	hits, total int
	score       float64
}

func (m contentMatch) percent() float64 {
	if m.total == 0 {
		return 0
	}
	return 100 * float64(m.hits) / float64(m.total)
}

// contentScore compares two fingerprints. total is the longer fingerprint
// length, which keeps the score comparable when one file yielded a few more
// samples than the other.
func contentScore(ref, dub fingerprint) contentMatch {
	total := max(len(ref.hashes), len(dub.hashes))
	m := contentMatch{total: total}
	if total == 0 {
		return m
	}
	for _, d := range dub.hashes {
		best := vhash.Side * vhash.Side
		for _, r := range ref.hashes {
			if d := vhash.Hamming(d, r); d < best {
				best = d
			}
		}
		if best < fingerprintDist {
			m.hits++
		}
	}
	m.score = float64(m.hits) / float64(total)
	return m
}

// contentChoice is one accepted reference/dub couple before the global
// one-to-one assignment.
type contentChoice struct {
	ref, dub string
	match    contentMatch
}

// chooseContent picks, for each reference, the dub whose fingerprint
// matches clearly better than every other. A candidate needs enough hits
// and a margin over the runner-up; without both, the name pairing is kept.
func chooseContent(refs, dubs []string, fps map[string]fingerprint) []contentChoice {
	type scored struct {
		dub   string
		match contentMatch
	}
	var choices []contentChoice
	for _, ref := range refs {
		rf, ok := fps[ref]
		if !ok || len(rf.hashes) == 0 {
			continue
		}
		cands := make([]scored, 0, len(dubs))
		for _, dub := range dubs {
			df, ok := fps[dub]
			if !ok || len(df.hashes) == 0 {
				continue
			}
			cands = append(cands, scored{dub: dub, match: contentScore(rf, df)})
		}
		if len(cands) == 0 {
			continue
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].match.hits != cands[j].match.hits {
				return cands[i].match.hits > cands[j].match.hits
			}
			return cands[i].dub < cands[j].dub
		})
		best := cands[0]
		second := 0
		if len(cands) > 1 {
			second = cands[1].match.hits
		}
		if best.match.hits < minFingerprintHits || best.match.hits < second+fingerprintMargin {
			continue
		}
		choices = append(choices, contentChoice{ref: ref, dub: best.dub, match: best.match})
	}
	return choices
}

// assignContent turns the per-reference choices into a one-to-one
// assignment, strongest match first. Conflicting weaker choices are
// dropped.
func assignContent(choices []contentChoice) map[string]contentChoice {
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].match.hits != choices[j].match.hits {
			return choices[i].match.hits > choices[j].match.hits
		}
		return choices[i].match.score > choices[j].match.score
	})
	usedRef := map[string]bool{}
	usedDub := map[string]bool{}
	out := map[string]contentChoice{}
	for _, c := range choices {
		if usedRef[c.ref] || usedDub[c.dub] {
			continue
		}
		out[c.ref] = c
		usedRef[c.ref] = true
		usedDub[c.dub] = true
	}
	return out
}

// weakPair reports whether a name pairing rests on an episode tag with a
// different title or on a fuzzy title. Such a pair is where shifted release
// numbering hides. Exact names, exact titles and manual pairs are strong.
func (t *tool) weakPair(p pair) bool {
	if p.how == "manual" {
		return false
	}
	suffix := t.cfg.DubSuffix
	if t.cfg.Mixed {
		suffix = ""
	}
	score, _, ok := pairScore(parseName(p.ref, ""), parseName(p.dub, suffix))
	return !ok || score < 0.95
}

// contentRepair verifies weak episode-tag pairs against the actual video
// and re-pairs them when another candidate matches clearly better. Strong
// name matches and manual pairs are never touched. Fingerprints are cached
// in the frame cache, so the extra decode pass happens once per file.
func (t *tool) contentRepair(plan pairing, refs, dubs []string) pairing {
	lockedRef := map[string]bool{}
	lockedDub := map[string]bool{}
	weakDub := map[string]string{}
	var workRefs []string
	for _, p := range plan.pairs {
		if t.weakPair(p) {
			weakDub[p.ref] = p.dub
			workRefs = append(workRefs, p.ref)
		} else {
			lockedRef[p.ref] = true
			lockedDub[p.dub] = true
		}
	}
	if len(plan.unmatchedDubs) > 0 {
		workRefs = append(workRefs, plan.unmatchedRefs...)
	}
	if len(workRefs) == 0 {
		return plan
	}

	var workDubs []string
	for _, dub := range dubs {
		if !lockedDub[dub] {
			workDubs = append(workDubs, dub)
		}
	}
	if len(workDubs) == 0 {
		return plan
	}

	fps := make(map[string]fingerprint, len(workRefs)+len(workDubs))
	failures := 0
	checked := 0
	total := len(workRefs) + len(workDubs)
	load := func(path string) {
		if _, ok := fps[path]; ok {
			return
		}
		checked++
		t.rep.Phase("content check %d/%d %s", checked, total, filepath.Base(path))
		f, err := t.fileFingerprint(path)
		if err != nil {
			failures++
			plan.notes = append(plan.notes, fmt.Sprintf("%s: content check unavailable: %v", filepath.Base(path), err))
			return
		}
		fps[path] = f
	}
	for _, ref := range workRefs {
		load(ref)
	}
	for _, dub := range workDubs {
		load(dub)
	}
	if failures == len(workRefs)+len(workDubs) {
		return plan
	}

	choices := chooseContent(workRefs, workDubs, fps)
	assigned := assignContent(choices)

	finalDub := map[string]string{}
	how := map[string]string{}
	for _, p := range plan.pairs {
		if !t.weakPair(p) {
			finalDub[p.ref] = p.dub
			how[p.ref] = p.how
		}
	}
	usedDub := map[string]bool{}
	for _, dub := range finalDub {
		usedDub[dub] = true
	}

	// Content wins over a weak name pair; a weak pair survives only when
	// neither side was claimed by a stronger content match.
	replaced := map[string]bool{}
	verified := map[string]bool{}
	for _, ref := range workRefs {
		if c, ok := assigned[ref]; ok && !usedDub[c.dub] {
			finalDub[ref] = c.dub
			usedDub[c.dub] = true
			original, hadPair := weakDub[ref]
			if hadPair && original == c.dub {
				how[ref] = fmt.Sprintf("content verified (%.0f%% frames)", c.match.percent())
				verified[filepath.Base(ref)] = true
				continue
			}
			how[ref] = fmt.Sprintf("content match (%.0f%% frames)", c.match.percent())
			action := "paired by content"
			if hadPair {
				action = "re-paired by content"
				replaced[filepath.Base(ref)] = true
			}
			note := fmt.Sprintf("%s <-> %s: %s (%.0f%% of sampled frames match)",
				filepath.Base(ref), filepath.Base(c.dub), action, c.match.percent())
			if t.episodeTagsDiffer(ref, c.dub) {
				note += " - episode numbers disagree"
			}
			plan.notes = append(plan.notes, note)
			continue
		}
		if original, ok := weakDub[ref]; ok && !usedDub[original] {
			finalDub[ref] = original
			usedDub[original] = true
		}
	}
	// A verified/re-paired couple no longer needs the generic warning that
	// an episode-tag match should be checked by hand.
	keptNotes := plan.notes[:0]
	for _, note := range plan.notes {
		weak := strings.Contains(note, "matched by episode tag only - verify the pairing")
		skip := false
		if weak {
			for base := range replaced {
				skip = skip || strings.HasPrefix(note, base+" <-> ")
			}
			for base := range verified {
				skip = skip || strings.HasPrefix(note, base+" <-> ")
			}
		}
		if !skip {
			keptNotes = append(keptNotes, note)
		}
	}
	plan.notes = keptNotes

	reportDubs := map[string]bool{}
	for _, dub := range plan.unmatchedDubs {
		reportDubs[dub] = true
	}
	for _, dub := range weakDub {
		reportDubs[dub] = true
	}
	rebuilt := pairing{suggestions: plan.suggestions, notes: plan.notes}
	usedRef := map[string]bool{}
	for _, ref := range refs {
		dub, ok := finalDub[ref]
		if !ok {
			continue
		}
		usedRef[ref] = true
		h := how[ref]
		if h == "" {
			for _, p := range plan.pairs {
				if p.ref == ref && p.dub == dub {
					h = p.how
					break
				}
			}
		}
		if h == "" {
			h = "content match"
		}
		rebuilt.pairs = append(rebuilt.pairs, pair{ref: ref, dub: dub, how: h})
	}
	for _, ref := range refs {
		if !usedRef[ref] && !lockedRef[ref] {
			rebuilt.unmatchedRefs = append(rebuilt.unmatchedRefs, ref)
		}
	}
	for _, dub := range dubs {
		if !usedDub[dub] && reportDubs[dub] {
			rebuilt.unmatchedDubs = append(rebuilt.unmatchedDubs, dub)
		}
	}
	sort.Strings(rebuilt.unmatchedRefs)
	sort.Strings(rebuilt.unmatchedDubs)
	return rebuilt
}

// fileFingerprint extracts (or loads from the frame cache) the sparse
// content signature of one video.
func (t *tool) fileFingerprint(path string) (fingerprint, error) {
	if !t.cfg.NoCache {
		if hashes, ok := t.cache.Load(path, fingerprintKey); ok {
			return fingerprint{hashes: hashes}, nil
		}
	}
	dur, err := t.fileDuration(path)
	if err != nil {
		return fingerprint{}, err
	}
	vf := fmt.Sprintf("fps=%g,scale=%d:%d,format=gray", float64(fingerprintFPS), vhash.Side, vhash.Side)
	hashes := make([][vhash.HashLen]byte, 0, fingerprintWindows*int(fingerprintWindow*fingerprintFPS))
	for i := 0; i < fingerprintWindows; i++ {
		frac := (float64(i) + 0.5) / fingerprintWindows
		start := math.Max(0, frac*dur-fingerprintWindow/2)
		out, err := t.r.Output(t.ffmpegBin,
			"-v", "error", "-nostdin",
			"-ss", strconv.FormatFloat(start, 'f', 3, 64),
			"-i", path,
			"-t", strconv.FormatFloat(fingerprintWindow, 'f', 1, 64),
			"-an", "-sn", "-dn",
			"-vf", vf,
			"-f", "rawvideo", "pipe:1")
		if err != nil {
			return fingerprint{}, fmt.Errorf("cannot sample frames: %w", err)
		}
		n := len(out) / vhash.FrameLen
		for k := 0; k < n; k++ {
			hashes = append(hashes, vhash.FrameHash(out[k*vhash.FrameLen:(k+1)*vhash.FrameLen]))
		}
	}
	if len(hashes) == 0 {
		return fingerprint{}, fmt.Errorf("no video frames found")
	}
	if !t.cfg.NoCache {
		if err := t.cache.Store(path, fingerprintKey, hashes); err != nil {
			t.rep.Raw(ui.WarnTone, "  warning: cannot cache the content fingerprint of %s: %v\n", filepath.Base(path), err)
		}
	}
	return fingerprint{hashes: hashes}, nil
}

// episodeTagsDiffer reports whether two paths carry different episode tags.
func (t *tool) episodeTagsDiffer(ref, dub string) bool {
	rk := parseName(ref, "")
	dk := parseName(dub, "")
	if !rk.hasEpisode || !dk.hasEpisode {
		return true
	}
	return rk.episode != dk.episode || (rk.hasSeason && dk.hasSeason && rk.season != dk.season)
}

// fileDuration returns the container duration, used to spread fingerprint
// windows over the file.
func (t *tool) fileDuration(path string) (float64, error) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		return 0, err
	}
	dur, ok := info.Duration()
	if !ok || dur <= 0 {
		return 0, fmt.Errorf("unknown duration")
	}
	return dur, nil
}

// pairingHasWeak reports whether any pair or unmatched file should be
// checked against the video content.
func pairingHasWeak(plan pairing, t *tool) bool {
	if len(plan.unmatchedRefs) > 0 || len(plan.unmatchedDubs) > 0 {
		return true
	}
	for _, p := range plan.pairs {
		if t.weakPair(p) {
			return true
		}
	}
	return false
}
