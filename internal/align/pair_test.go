package align

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	tests := []struct {
		path    string
		title   string
		season  int
		episode int
		year    int
		multi   bool
	}{
		{"Show.S06E02.1080p.WEB-DL.x265.mkv", "show", 6, 2, 0, false},
		{"Show.6x02.CZ.mkv", "show", 6, 2, 0, false},
		{"Show Season 6 Episode 2.mkv", "show", 6, 2, 0, false},
		{"Show.S01E01E02.mkv", "show", 1, 1, 0, true},
		{"The.Movie.2019.1080p.mkv", "the movie", 0, 0, 2019, false},
		{"The Movie (2019) CZ.mkv", "the movie", 0, 0, 2019, false},
		{"Amelie.2001.CZ.mkv", "amelie", 0, 0, 2001, false},
	}
	for _, tt := range tests {
		k := parseName(filepath.FromSlash(tt.path), "")
		if k.title != tt.title || k.season != tt.season || k.episode != tt.episode || k.year != tt.year || k.multi != tt.multi {
			t.Errorf("parseName(%q) = %+v, want title=%q s=%d e=%d year=%d multi=%v",
				tt.path, k, tt.title, tt.season, tt.episode, tt.year, tt.multi)
		}
	}
}

func TestParseNameContextFolder(t *testing.T) {
	path := filepath.FromSlash("/lib/Show/Season 1/S01E02.mkv")
	k := parseName(path, "")
	if k.title != "show" || k.season != 1 || k.episode != 2 {
		t.Errorf("parseName(%q) = %+v, want show s01e02 from the folder", path, k)
	}
	// bare E02 inside a season folder: the folder supplies the season
	path = filepath.FromSlash("/lib/Show/Season 3/E02.mkv")
	k = parseName(path, "")
	if k.title != "show" || k.season != 3 || k.episode != 2 || !k.hasSeason {
		t.Errorf("parseName(%q) = %+v, want show s03e02 from the folder", path, k)
	}
}

func TestPairFilesEpisodeVariants(t *testing.T) {
	refs := []string{
		filepath.FromSlash("/en/Show.S06E02.1080p.WEB-DL.x265.mkv"),
	}
	dubs := []string{
		filepath.FromSlash("/cz/Show.6x02.CZ.mkv"),
	}
	p, err := pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 1 || p.pairs[0].ref != refs[0] || p.pairs[0].dub != dubs[0] {
		t.Fatalf("pairs = %+v, want the episode match", p.pairs)
	}
	if len(p.unmatchedRefs) != 0 || len(p.unmatchedDubs) != 0 {
		t.Errorf("unexpected unmatched: %+v / %+v", p.unmatchedRefs, p.unmatchedDubs)
	}
}

func TestPairFilesEpisodeAlone(t *testing.T) {
	// the show names differ completely: the episode tag alone must pair,
	// with a notice
	refs := []string{filepath.FromSlash("/en/ShowA.S02E05.mkv")}
	dubs := []string{filepath.FromSlash("/cz/Completely.Different.02x05.CZ.mkv")}
	p, err := pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 1 {
		t.Fatalf("pairs = %+v, want the episode-tag pair", p.pairs)
	}
	if !strings.Contains(p.pairs[0].how, "episode s02e05") || !strings.Contains(p.pairs[0].how, "title differs") {
		t.Errorf("pair reason must report the differing title: %q", p.pairs[0].how)
	}
	if len(p.notes) == 0 {
		t.Error("episode-only pairing must carry a verify notice")
	}

	// single-digit spellings are the same episode key
	refs = []string{filepath.FromSlash("/en/Show.S2E5.mkv")}
	dubs = []string{filepath.FromSlash("/cz/Show.2x5.mkv")}
	p, err = pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 1 {
		t.Fatalf("S2E5 vs 2x5: pairs = %+v, want one pair", p.pairs)
	}

	// a different season must not pair by episode alone
	refs = []string{filepath.FromSlash("/en/Show.S02E05.mkv")}
	dubs = []string{filepath.FromSlash("/cz/Show.S03E05.mkv")}
	p, err = pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 0 {
		t.Fatalf("season mismatch must not pair: %+v", p.pairs)
	}
}

func TestPairFilesAmbiguousDifferentShows(t *testing.T) {
	refs := []string{filepath.FromSlash("/en/ShowC.S02E05.mkv")}
	dubs := []string{
		filepath.FromSlash("/cz/ShowA.02x05.mkv"),
		filepath.FromSlash("/cz/ShowB.02x05.mkv"),
	}
	p, err := pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 0 {
		t.Fatalf("several same-episode candidates must stay ambiguous: %+v", p.pairs)
	}
	if len(p.notes) == 0 {
		t.Error("ambiguous same-episode candidates must carry a note")
	}
}

func TestPairFilesMovie(t *testing.T) {
	refs := []string{filepath.FromSlash("/en/The.Movie.2019.1080p.mkv")}
	dubs := []string{filepath.FromSlash("/cz/The Movie (2019) CZ.mkv")}
	p, err := pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 1 {
		t.Fatalf("pairs = %+v, want one title+year pair", p.pairs)
	}
	if p.pairs[0].how == "" {
		t.Error("pairing reason must be set")
	}
}

func TestPairFilesMissingAndSuggestion(t *testing.T) {
	refs := []string{
		filepath.FromSlash("/en/Show.S06E02.mkv"),
		filepath.FromSlash("/en/Show.S06E04.mkv"),
	}
	dubs := []string{
		filepath.FromSlash("/cz/Show.6x02.CZ.mkv"),
		filepath.FromSlash("/cz/Show.6x05.CZ.mkv"),
	}
	p, err := pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 1 || p.pairs[0].ref != refs[0] {
		t.Fatalf("pairs = %+v, want the s06e02 couple", p.pairs)
	}
	if len(p.unmatchedRefs) != 1 || p.unmatchedRefs[0] != refs[1] {
		t.Fatalf("unmatched refs = %+v, want the s06e04 file", p.unmatchedRefs)
	}
	if _, ok := p.suggestions[refs[1]]; !ok {
		t.Errorf("missing counterpart must carry a suggestion, got %+v", p.suggestions)
	}
}

func TestPairFilesAmbiguous(t *testing.T) {
	refs := []string{filepath.FromSlash("/en/Show.S01E01.mkv")}
	dubs := []string{
		filepath.FromSlash("/cz/Show.1x01.A.mkv"),
		filepath.FromSlash("/cz/Show.1x01.B.mkv"),
	}
	p, err := pairFiles(refs, dubs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 0 {
		t.Fatalf("ambiguous names must not pair automatically: %+v", p.pairs)
	}
	if len(p.notes) == 0 {
		t.Error("ambiguous names must carry a note")
	}
	if len(p.unmatchedRefs) != 1 {
		t.Errorf("unmatched refs = %+v", p.unmatchedRefs)
	}
}

func TestPairFilesManual(t *testing.T) {
	refs := []string{filepath.FromSlash("/en/a.mkv"), filepath.FromSlash("/en/b.mkv")}
	dubs := []string{filepath.FromSlash("/cz/x.mkv"), filepath.FromSlash("/cz/y.mkv")}
	p, err := pairFiles(refs, dubs, []string{"a.mkv=x.mkv", "b.mkv=y.mkv"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 2 {
		t.Fatalf("pairs = %+v, want both manual pairs", p.pairs)
	}
	for _, pr := range p.pairs {
		if pr.how != "manual" {
			t.Errorf("pair %+v must be marked manual", pr)
		}
	}
	if _, err := pairFiles(refs, dubs, []string{"a.mkv=missing.mkv"}, ""); err == nil {
		t.Fatal("unknown manual target must fail")
	}
}

func TestPairFilesDubSuffix(t *testing.T) {
	refs := []string{
		filepath.FromSlash("/lib/Movie.2019.mkv"),
		filepath.FromSlash("/lib/Show.S01E01.mkv"),
	}
	dubs := []string{
		filepath.FromSlash("/lib/Movie.2019.cz.mkv"),
		filepath.FromSlash("/lib/Show.S01E01.cz.mkv"),
	}
	p, err := pairFiles(refs, dubs, nil, ".cz")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.pairs) != 2 {
		t.Fatalf("pairs = %+v, want both suffix pairs", p.pairs)
	}
}

func TestDubSuffix(t *testing.T) {
	for _, tt := range []struct {
		path, suffix string
		want         bool
	}{
		{"/lib/movie.cz.mkv", ".cz", true},
		{"/lib/movie.CZ.MKV", ".cz", true},
		{"/lib/movie.czech.mkv", ".cz", false},
		{"/lib/movie.mkv", ".cz", false},
		{"/lib/show.s01e01.cz.mkv", ".cz", true},
		{"/lib/show.s01e01.mkv", "", false},
	} {
		if got := hasDubSuffix(filepath.FromSlash(tt.path), tt.suffix); got != tt.want {
			t.Errorf("hasDubSuffix(%q, %q) = %v, want %v", tt.path, tt.suffix, got, tt.want)
		}
	}
}

func TestPairOne(t *testing.T) {
	// reference file + dub directory: the episode tag selects the dub
	p := pairOne(filepath.FromSlash("/en/ShowA.S02E05.mkv"), []string{
		filepath.FromSlash("/cz/Other.02x05.CZ.mkv"),
		filepath.FromSlash("/cz/ShowA.S02E06.CZ.mkv"),
	}, true)
	if len(p.pairs) != 1 {
		t.Fatalf("pairs = %+v, want one", p.pairs)
	}
	if p.pairs[0].dub != filepath.FromSlash("/cz/Other.02x05.CZ.mkv") {
		t.Errorf("selected dub = %s, want 02x05", p.pairs[0].dub)
	}
	if len(p.notes) == 0 {
		t.Error("episode-only match must carry a note")
	}

	// dub file + reference directory: the matching reference is selected
	p = pairOne(filepath.FromSlash("/cz/Other.02x05.CZ.mkv"), []string{
		filepath.FromSlash("/en/ShowA.S02E05.mkv"),
		filepath.FromSlash("/en/ShowA.S02E06.mkv"),
	}, false)
	if len(p.pairs) != 1 || p.pairs[0].ref != filepath.FromSlash("/en/ShowA.S02E05.mkv") {
		t.Fatalf("pairs = %+v, want S02E05 as the reference", p.pairs)
	}

	// no episode match: skip with a closest-name suggestion
	p = pairOne(filepath.FromSlash("/en/Show.S02E05.mkv"), []string{
		filepath.FromSlash("/cz/Show.S02E07.CZ.mkv"),
	}, true)
	if len(p.pairs) != 0 || len(p.unmatchedRefs) != 1 {
		t.Fatalf("pairs = %+v, unmatched = %+v; want a skip", p.pairs, p.unmatchedRefs)
	}
	if _, ok := p.suggestions[filepath.FromSlash("/en/Show.S02E05.mkv")]; !ok {
		t.Errorf("missing closest-dub suggestion: %+v", p.suggestions)
	}
}
