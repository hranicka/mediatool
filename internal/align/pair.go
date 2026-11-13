package align

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// pair is one reference/dub couple and how it was found.
type pair struct {
	ref, dub, how string
}

// suggestion is the closest dub for an unmatched reference.
type suggestion struct {
	path  string
	score float64
	kind  string
}

// candidate is one possible counterpart with its score and reason.
type candidate struct {
	path  string
	score float64
	how   string
}

// chooseCandidate sorts the candidates and applies the pairing policy:
// exact/unique/fuzzy best wins; several close candidates stay ambiguous.
func chooseCandidate(cands []candidate) (chosen candidate, ok, ambiguous bool, best float64) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].path < cands[j].path
	})
	if len(cands) == 0 {
		return candidate{}, false, false, 0
	}
	best = cands[0].score
	second := 0.0
	if len(cands) > 1 {
		second = cands[1].score
	}
	if len(cands) > 1 && best-second < 0.02 {
		return cands[0], false, true, best
	}
	if best >= 0.98 || (len(cands) == 1 && best >= 0.6) || (best >= 0.9 && best-second >= 0.05) {
		return cands[0], true, false, best
	}
	return cands[0], false, false, best
}

// pairing is the batch plan: matched couples, unmatched files on both
// sides, closest-match suggestions for the unmatched references and notes
// about ambiguous names.
type pairing struct {
	pairs         []pair
	unmatchedRefs []string
	unmatchedDubs []string
	suggestions   map[string]suggestion
	notes         []string
}

// nameKey is the normalized naming identity of one file: a movie title
// (+year) or a show title + episode tag.
type nameKey struct {
	stem       string // normalized full stem, noise stripped
	title      string // show (before the episode tag) or movie title
	year       int
	season     int
	episode    int
	hasEpisode bool
	hasSeason  bool
	multi      bool // several episode tags in one name
}

var (
	reSxxExx    = regexp.MustCompile(`\bs(\d{1,2})[ ._-]*e(\d{1,3})((?:[ ._-]*e\d{1,3})+)?\b`)
	reNxNN      = regexp.MustCompile(`\b(\d{1,2})x(\d{1,3})\b`)
	reSeasonEp  = regexp.MustCompile(`\bseason[ ._-]*(\d{1,2})[ ._-]*(?:episode|ep)[ ._-]*(\d{1,3})\b`)
	reEpisode   = regexp.MustCompile(`\b(?:episode|ep)[ ._-]*(\d{1,3})\b`)
	reBareEp    = regexp.MustCompile(`\be[ ._-]*(\d{1,3})\b`)
	reSeasonDir = regexp.MustCompile(`\bseason[ ._-]*(\d{1,2})\b`)
	reSNumDir   = regexp.MustCompile(`\bs(\d{1,2})\b`)
	reYear      = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	noiseTokens = map[string]bool{
		"1080p": true, "1080i": true, "720p": true, "576p": true, "480p": true,
		"2160p": true, "4k": true, "uhd": true, "hd": true, "sd": true,
		"bluray": true, "bdrip": true, "brrip": true, "webrip": true, "webdl": true,
		"web": true, "hdtv": true, "dvdrip": true, "dvd": true, "remux": true,
		"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true,
		"avc": true, "av1": true, "aac": true, "ac3": true, "eac3": true,
		"dts": true, "truehd": true, "flac": true, "atmos": true,
		"10bit": true, "8bit": true, "hdr": true, "dv": true, "repack": true,
		"proper": true, "extended": true, "uncut": true, "remastered": true,
		"internal": true, "limited": true, "custom": true,
	}
	langTokens = map[string]bool{
		"cz": true, "czech": true, "ces": true, "cze": true,
		"en": true, "eng": true, "english": true,
		"dabing": true, "dab": true, "dual": true, "cztit": true,
		"titulky": true, "subs": true, "subtitles": true,
	}
	genericDirs = map[string]bool{
		"movies": true, "movie": true, "tv": true,
		"video": true, "videos": true, "media": true, "downloads": true,
		"complete": true, "extras": true, "featurettes": true, "sample": true,
		"en": true, "cz": true, "english": true, "czech": true, "dabing": true,
	}
)

// pairFiles matches the reference and dub files by exact normalized name,
// episode tag or movie title. Manual ref=dub entries win over everything.
func pairFiles(refs, dubs []string, manual []string, dubSuffix string) (pairing, error) {
	p := pairing{suggestions: map[string]suggestion{}}
	refKeys := make(map[string]nameKey, len(refs))
	dubKeys := make(map[string]nameKey, len(dubs))
	for _, r := range refs {
		refKeys[r] = parseName(r, "")
	}
	for _, d := range dubs {
		dubKeys[d] = parseName(d, dubSuffix)
	}

	usedRef := map[string]bool{}
	usedDub := map[string]bool{}
	for _, spec := range manual {
		refSpec, dubSpec, ok := strings.Cut(spec, "=")
		if !ok || refSpec == "" || dubSpec == "" {
			return p, fmt.Errorf("-pair %q: want ref=dub", spec)
		}
		ref, err := resolveListed(refSpec, refs)
		if err != nil {
			return p, fmt.Errorf("-pair %q: %w", spec, err)
		}
		dub, err := resolveListed(dubSpec, dubs)
		if err != nil {
			return p, fmt.Errorf("-pair %q: %w", spec, err)
		}
		if usedRef[ref] {
			return p, fmt.Errorf("-pair %q: reference %s is already paired", spec, ref)
		}
		if usedDub[dub] {
			return p, fmt.Errorf("-pair %q: dub %s is already paired", spec, dub)
		}
		p.pairs = append(p.pairs, pair{ref: ref, dub: dub, how: "manual"})
		usedRef[ref], usedDub[dub] = true, true
	}

	for _, ref := range refs {
		if usedRef[ref] {
			continue
		}
		rk := refKeys[ref]
		var cands []candidate
		for _, dub := range dubs {
			if dub == ref || usedDub[dub] {
				continue
			}
			dk := dubKeys[dub]
			score, how, ok := pairScore(rk, dk)
			if ok {
				cands = append(cands, candidate{path: dub, score: score, how: how})
			}
		}
		chosen, ok, ambiguous, _ := chooseCandidate(cands)
		switch {
		case rk.multi:
			p.notes = append(p.notes, fmt.Sprintf("%s: multi-episode names are not auto-paired, use -pair", filepath.Base(ref)))
			p.unmatchedRefs = append(p.unmatchedRefs, ref)
		case ambiguous:
			p.notes = append(p.notes, fmt.Sprintf("%s: ambiguous candidates %s and %s", filepath.Base(ref), filepath.Base(cands[0].path), filepath.Base(cands[1].path)))
			p.unmatchedRefs = append(p.unmatchedRefs, ref)
		case ok:
			how := chosen.how
			if strings.HasPrefix(how, "episode ") && chosen.score < 0.95 {
				p.notes = append(p.notes, fmt.Sprintf("%s <-> %s: matched by episode tag only - verify the pairing",
					filepath.Base(ref), filepath.Base(chosen.path)))
			}
			if chosen.score < 0.98 && how != "manual" {
				how = fmt.Sprintf("%s, score %.2f", how, chosen.score)
			}
			p.pairs = append(p.pairs, pair{ref: ref, dub: chosen.path, how: how})
			usedRef[ref], usedDub[chosen.path] = true, true
		default:
			if len(cands) > 0 {
				p.suggestions[ref] = suggestion{path: cands[0].path, score: cands[0].score, kind: cands[0].how}
			}
			p.unmatchedRefs = append(p.unmatchedRefs, ref)
		}
	}
	for _, ref := range p.unmatchedRefs {
		if _, ok := p.suggestions[ref]; ok {
			continue
		}
		rk := refKeys[ref]
		bestPath, bestScore := "", 0.0
		for _, dub := range dubs {
			if usedDub[dub] {
				continue
			}
			if score := looseScore(rk, dubKeys[dub]); score > bestScore {
				bestPath, bestScore = dub, score
			}
		}
		if bestScore >= 0.5 {
			p.suggestions[ref] = suggestion{path: bestPath, score: bestScore, kind: "name"}
		}
	}
	for _, dub := range dubs {
		if !usedDub[dub] {
			p.unmatchedDubs = append(p.unmatchedDubs, dub)
		}
	}
	sort.Strings(p.unmatchedRefs)
	sort.Strings(p.unmatchedDubs)
	return p, nil
}

// pairOne matches one explicit file against the candidates from the other
// directory, using the same rules as directory pairing. refIsSingle says
// the single file is the reference (candidates are dubs); otherwise the
// single file is the dub and the candidates are references.
func pairOne(single string, candidates []string, refIsSingle bool) pairing {
	p := pairing{suggestions: map[string]suggestion{}}
	sk := parseName(single, "")
	if sk.multi {
		p.notes = append(p.notes, fmt.Sprintf("%s: multi-episode names are not auto-paired, use -pair", filepath.Base(single)))
		if refIsSingle {
			p.unmatchedRefs = []string{single}
		} else {
			p.unmatchedDubs = []string{single}
		}
		return p
	}
	var cands []candidate
	for _, c := range candidates {
		if c == single {
			continue
		}
		ck := parseName(c, "")
		var score float64
		var how string
		var ok bool
		if refIsSingle {
			score, how, ok = pairScore(sk, ck)
		} else {
			score, how, ok = pairScore(ck, sk)
		}
		if ok {
			cands = append(cands, candidate{path: c, score: score, how: how})
		}
	}
	chosen, ok, ambiguous, best := chooseCandidate(cands)
	if !ok {
		if ambiguous {
			p.notes = append(p.notes, fmt.Sprintf("%s: ambiguous candidates %s and %s", filepath.Base(single), filepath.Base(cands[0].path), filepath.Base(cands[1].path)))
		}
		if len(cands) == 0 {
			// no episode/title key match: still offer the closest name
			bestPath, bestScore := "", 0.0
			for _, c := range candidates {
				ck := parseName(c, "")
				var score float64
				if refIsSingle {
					score = looseScore(sk, ck)
				} else {
					score = looseScore(ck, sk)
				}
				if score > bestScore {
					bestPath, bestScore = c, score
				}
			}
			if bestScore >= 0.5 {
				if refIsSingle {
					p.suggestions[single] = suggestion{path: bestPath, score: bestScore, kind: "name"}
				} else {
					p.notes = append(p.notes, fmt.Sprintf("%s: closest reference %s (name score %.2f)", filepath.Base(single), filepath.Base(bestPath), bestScore))
				}
			}
		}
		if len(cands) > 0 {
			if refIsSingle {
				p.suggestions[single] = suggestion{path: cands[0].path, score: best, kind: cands[0].how}
			} else {
				p.notes = append(p.notes, fmt.Sprintf("%s: closest reference %s (score %.2f)", filepath.Base(single), filepath.Base(cands[0].path), best))
			}
		}
		if refIsSingle {
			p.unmatchedRefs = []string{single}
		} else {
			p.unmatchedDubs = []string{single}
		}
		return p
	}
	how := chosen.how
	refPath, dubPath := chosen.path, single
	if refIsSingle {
		refPath, dubPath = single, chosen.path
	}
	if strings.HasPrefix(how, "episode ") && chosen.score < 0.95 {
		p.notes = append(p.notes, fmt.Sprintf("%s <-> %s: matched by episode tag only - verify the pairing",
			filepath.Base(refPath), filepath.Base(dubPath)))
	}
	if chosen.score < 0.98 && how != "manual" {
		how = fmt.Sprintf("%s, score %.2f", how, chosen.score)
	}
	if refIsSingle {
		p.pairs = []pair{{ref: single, dub: chosen.path, how: how}}
	} else {
		p.pairs = []pair{{ref: chosen.path, dub: single, how: how}}
	}
	return p
}

// pairScore rates one reference/dub couple. ok=false means the episode or
// year contradicts; a low score is still returned so the caller can show a
// suggestion.
func pairScore(r, d nameKey) (score float64, how string, ok bool) {
	if r.hasEpisode || d.hasEpisode {
		if !r.hasEpisode || !d.hasEpisode || r.multi || d.multi {
			return 0, "", false
		}
		if r.episode != d.episode {
			return 0, "", false
		}
		if r.hasSeason && d.hasSeason && r.season != d.season {
			return 0, "", false
		}
		// A matching episode tag is strong enough evidence on its own:
		// release groups spell the show differently all the time. The
		// title similarity only separates several same-episode candidates.
		titleSim := similarity(r.title, d.title)
		if r.title == "" || d.title == "" {
			titleSim = max(titleSim, 0.5)
		}
		score = 0.9 + 0.1*titleSim
		how := fmt.Sprintf("episode e%02d", r.episode)
		if r.hasSeason {
			how = fmt.Sprintf("episode s%02de%02d", r.season, r.episode)
		}
		if titleSim < 0.5 {
			how += " (title differs)"
		}
		return score, how, true
	}
	if r.year != 0 && d.year != 0 && r.year != d.year {
		return 0, "", false
	}
	score = similarity(r.title, d.title)
	if score < 0.3 {
		return score, "title", false
	}
	if r.year != 0 {
		return score, fmt.Sprintf("title+year %d", r.year), true
	}
	return score, "title", true
}

// looseScore is a name-only similarity used for suggestions when the
// episode/year keys contradict.
func looseScore(r, d nameKey) float64 {
	score := similarity(r.title, d.title)
	if score < 0.5 {
		score = max(score, similarity(r.stem, d.stem))
	}
	return score
}

// resolveListed finds a manual pairing argument by full path, base name or
// normalized stem; ambiguity is an error.
func resolveListed(spec string, list []string) (string, error) {
	if containsPath(list, spec) {
		return spec, nil
	}
	wantBase := filepath.Base(spec)
	var matches []string
	for _, path := range list {
		if filepath.Base(path) == wantBase {
			matches = append(matches, path)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("%s matches several files: %s", spec, strings.Join(matches, ", "))
	}
	wantStem := normalizeName(strings.TrimSuffix(wantBase, filepath.Ext(wantBase)))
	for _, path := range list {
		base := filepath.Base(path)
		if normalizeName(strings.TrimSuffix(base, filepath.Ext(base))) == wantStem {
			matches = append(matches, path)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", fmt.Errorf("%s not found in the walked files", spec)
}

func containsPath(list []string, path string) bool {
	for _, p := range list {
		if p == path {
			return true
		}
	}
	return false
}

// parseName extracts the pairing key from a file name. contextTitle fills
// in the title from parent folders when the file name only carries an
// episode tag.
func parseName(path, dubSuffix string) nameKey {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if dubSuffix != "" && strings.HasSuffix(strings.ToLower(stem), strings.ToLower(dubSuffix)) {
		stem = stem[:len(stem)-len(dubSuffix)]
	}
	normalized := normalizeName(stem)
	tokens := stripNoise(strings.Fields(normalized))
	k := nameKey{stem: strings.Join(stripTrailingLang(tokens), " ")}

	var (
		loc       []int
		season    int
		episode   int
		hasSeason bool
		multi     bool
		titlePart string
	)
	for _, re := range []*regexp.Regexp{reSxxExx, reNxNN, reSeasonEp} {
		if m := re.FindStringSubmatchIndex(normalized); m != nil {
			loc = m
			season, _ = strconv.Atoi(normalized[m[2]:m[3]])
			episode, _ = strconv.Atoi(normalized[m[4]:m[5]])
			hasSeason = true
			multi = re == reSxxExx && m[6] >= 0 && m[7] > m[6]
			break
		}
	}
	if loc == nil {
		if m := reEpisode.FindStringSubmatchIndex(normalized); m != nil {
			loc = m
			episode, _ = strconv.Atoi(normalized[m[2]:m[3]])
		}
	}
	if loc == nil {
		if m := reBareEp.FindStringSubmatchIndex(normalized); m != nil {
			loc = m
			episode, _ = strconv.Atoi(normalized[m[2]:m[3]])
		}
	}
	if loc != nil {
		k.hasEpisode, k.hasSeason, k.multi = true, hasSeason, multi
		k.season, k.episode = season, episode
		titlePart = normalized[:loc[0]]
	} else {
		titlePart = normalized
	}
	if k.hasEpisode && !k.hasSeason {
		if s, ok := contextSeason(path); ok {
			k.season, k.hasSeason = s, true
		}
	}
	title := strings.Join(stripTrailingLang(stripNoise(strings.Fields(titlePart))), " ")
	if title == "" {
		title = contextTitle(path)
	}
	k.title = title
	if !k.hasEpisode {
		if m := reYear.FindStringSubmatchIndex(normalized); m != nil {
			k.year, _ = strconv.Atoi(normalized[m[2]:m[3]])
			titleYearless := normalized[:m[0]] + " " + normalized[m[1]:]
			k.title = strings.Join(stripTrailingLang(stripNoise(strings.Fields(titleYearless))), " ")
			if k.title == "" {
				k.title = contextTitle(path)
			}
		}
	}
	return k
}

// contextTitle derives a title from the nearest non-generic parent folder.
func contextTitle(path string) string {
	dir := filepath.Dir(path)
	for depth := 0; depth < 3; depth++ {
		base := normalizeName(filepath.Base(dir))
		tokens := stripNoise(strings.Fields(base))
		if len(tokens) > 0 && !genericDirs[strings.Join(tokens, " ")] && !isSeasonDir(tokens) {
			title := strings.Join(stripTrailingLang(tokens), " ")
			if title != "" {
				return title
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// contextSeason derives a season number from the nearest "Season N" or
// "S N" parent folder, so bare E02 names work inside season directories.
func contextSeason(path string) (int, bool) {
	dir := filepath.Dir(path)
	for depth := 0; depth < 3; depth++ {
		base := normalizeName(filepath.Base(dir))
		if m := reSeasonDir.FindStringSubmatch(base); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, true
		}
		if m := reSNumDir.FindStringSubmatch(base); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return 0, false
}

func isSeasonDir(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	joined := strings.Join(tokens, " ")
	if reSeasonEp.MatchString(joined) || reSxxExx.MatchString(joined) || reNxNN.MatchString(joined) {
		return true
	}
	return strings.HasPrefix(joined, "season ") || strings.HasPrefix(joined, "series ")
}

// normalizeName lowercases and turns every non-alphanumeric rune into a
// space, preserving accented letters in titles.
func normalizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func stripNoise(tokens []string) []string {
	out := tokens[:0]
	for _, t := range tokens {
		if noiseTokens[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func stripTrailingLang(tokens []string) []string {
	for len(tokens) > 0 && langTokens[tokens[len(tokens)-1]] {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

// similarity scores two normalized titles: the better of token overlap
// and edit-distance ratio. Exact equality always scores 1.
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	ja, jb := tokenJaccard(a, b), editRatio(a, b)
	return math.Max(ja, jb)
}

func tokenJaccard(a, b string) float64 {
	as := strings.Fields(a)
	bs := strings.Fields(b)
	if len(as) == 0 || len(bs) == 0 {
		return 0
	}
	set := make(map[string]bool, len(as))
	for _, t := range as {
		set[t] = true
	}
	inter := 0
	for _, t := range bs {
		if set[t] {
			inter++
			delete(set, t)
		}
	}
	union := len(as) + len(bs) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func editRatio(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	longest := max(len(ar), len(br))
	if longest == 0 {
		return 0
	}
	return 1 - float64(prev[len(br)])/float64(longest)
}
