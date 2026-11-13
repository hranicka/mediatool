package align

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

func fakeHashes(seed byte, n int) [][vhash.HashLen]byte {
	out := make([][vhash.HashLen]byte, n)
	for i := range out {
		for j := range out[i] {
			out[i][j] = seed + byte(i*7+j*13)
		}
	}
	return out
}

func TestContentScore(t *testing.T) {
	ref := fingerprint{hashes: fakeHashes(10, 12)}
	same := fingerprint{hashes: fakeHashes(10, 12)}
	other := fingerprint{hashes: fakeHashes(200, 12)}
	clip := fingerprint{hashes: fakeHashes(10, 6)}

	if got := contentScore(ref, same); got.hits != 12 || got.score != 1 {
		t.Errorf("same fingerprint: got %d hits, score %.2f, want 12/1", got.hits, got.score)
	}
	if got := contentScore(ref, other); got.hits != 0 {
		t.Errorf("different fingerprint: got %d hits, want 0", got.hits)
	}
	// A short excerpt must not look like a full episode: the longer side is
	// the denominator.
	if got := contentScore(ref, clip); got.hits != 6 || got.score != 0.5 {
		t.Errorf("short excerpt: got %d hits, score %.2f, want 6/0.5", got.hits, got.score)
	}
}

func TestChooseContentFollowsVideoNotNumbers(t *testing.T) {
	a := fingerprint{hashes: fakeHashes(10, 12)}
	b := fingerprint{hashes: fakeHashes(80, 12)}
	c := fingerprint{hashes: fakeHashes(150, 12)}
	refs := []string{"ref-a", "ref-b", "ref-c"}
	// The dubbed files are numbered in the same order but hold a rotated
	// set of episodes.
	dubs := []string{"dub-a", "dub-b", "dub-c"}
	fps := map[string]fingerprint{
		"ref-a": a, "ref-b": b, "ref-c": c,
		"dub-a": b, "dub-b": c, "dub-c": a,
	}
	assigned := assignContent(chooseContent(refs, dubs, fps))
	want := map[string]string{"ref-a": "dub-c", "ref-b": "dub-a", "ref-c": "dub-b"}
	for ref, dub := range want {
		got, ok := assigned[ref]
		if !ok || got.dub != dub {
			t.Errorf("%s: got %q, want %q", ref, got.dub, dub)
		}
	}
}

func TestChooseContentRejectsAmbiguous(t *testing.T) {
	a := fingerprint{hashes: fakeHashes(10, 12)}
	b := fingerprint{hashes: fakeHashes(80, 12)}
	fps := map[string]fingerprint{
		"ref": a,
		// Both dubs contain the reference: only a clear margin may pair.
		"dub-a": a,
		"dub-b": a,
	}
	if got := chooseContent([]string{"ref"}, []string{"dub-a", "dub-b"}, fps); len(got) != 0 {
		t.Fatalf("ambiguous pair must not be guessed: %+v", got)
	}
	if got := chooseContent([]string{"ref"}, []string{"dub-a"}, map[string]fingerprint{
		"ref": a, "dub-a": b,
	}); len(got) != 0 {
		t.Fatalf("a weak match must not pair: %+v", got)
	}
}

func TestContentRepair(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	cache := vhash.Cache{Dir: cacheDir}
	refs := []string{
		filepath.Join(dir, "Alfa.S01E01.mkv"),
		filepath.Join(dir, "Alfa.S01E02.mkv"),
		filepath.Join(dir, "Alfa.S01E03.mkv"),
	}
	dubs := []string{
		filepath.Join(dir, "Beta S01E01.mkv"),
		filepath.Join(dir, "Beta S01E02.mkv"),
		filepath.Join(dir, "Beta S01E03.mkv"),
	}
	hashes := map[string][][vhash.HashLen]byte{
		refs[0]: fakeHashes(10, 12),
		refs[1]: fakeHashes(80, 12),
		refs[2]: fakeHashes(150, 12),
	}
	// Rotated content: the Czech numbering is shifted by one episode.
	hashes[dubs[0]] = hashes[refs[1]]
	hashes[dubs[1]] = hashes[refs[2]]
	hashes[dubs[2]] = hashes[refs[0]]
	for _, path := range append(append([]string{}, refs...), dubs...) {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for path, h := range hashes {
		if err := cache.Store(path, fingerprintKey, h); err != nil {
			t.Fatal(err)
		}
	}

	rep := ui.New(io.Discard, ui.Profile{})
	tl := &tool{cfg: &Config{}, cache: cache, rep: rep}
	plan := pairing{
		pairs: []pair{
			{ref: refs[0], dub: dubs[0], how: "episode s01e01 (title differs), score 0.90"},
			{ref: refs[1], dub: dubs[1], how: "episode s01e02 (title differs), score 0.90"},
			{ref: refs[2], dub: dubs[2], how: "episode s01e03 (title differs), score 0.90"},
		},
		suggestions: map[string]suggestion{},
		notes: []string{
			filepath.Base(refs[0]) + " <-> " + filepath.Base(dubs[0]) + ": matched by episode tag only - verify the pairing",
			filepath.Base(refs[1]) + " <-> " + filepath.Base(dubs[1]) + ": matched by episode tag only - verify the pairing",
			filepath.Base(refs[2]) + " <-> " + filepath.Base(dubs[2]) + ": matched by episode tag only - verify the pairing",
		},
	}
	got := tl.contentRepair(plan, refs, dubs)
	want := map[string]string{refs[0]: dubs[2], refs[1]: dubs[0], refs[2]: dubs[1]}
	if len(got.pairs) != 3 {
		t.Fatalf("pairs: got %d, want 3 (%+v)", len(got.pairs), got.pairs)
	}
	for _, p := range got.pairs {
		if want[p.ref] != p.dub {
			t.Errorf("%s: got %s, want %s", filepath.Base(p.ref), filepath.Base(p.dub), filepath.Base(want[p.ref]))
		}
		if !strings.HasPrefix(p.how, "content match") {
			t.Errorf("%s: how %q, want content match", filepath.Base(p.ref), p.how)
		}
	}
	for _, note := range got.notes {
		if strings.Contains(note, "matched by episode tag only") {
			t.Errorf("stale warning after a content repair: %s", note)
		}
	}
}
