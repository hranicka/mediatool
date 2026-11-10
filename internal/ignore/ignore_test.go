package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUnion(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".media-tool-ignore"), "trailers\n# a comment\n\nsub/\r\n")
	write(t, filepath.Join(dir, ".cleaner-ignore"), "private\n")

	l := Load(dir, "clean", []string{"CLI", "extra"})
	got := l.entries
	want := []string{"trailers", "sub/", "private", "CLI", "extra"}
	if len(got) != len(want) {
		t.Fatalf("entries: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries: got %v, want %v", got, want)
		}
	}
	if l.Len() != 5 {
		t.Errorf("Len: got %d, want 5", l.Len())
	}
}

func TestLoadMissingFiles(t *testing.T) {
	l := Load(t.TempDir(), "dup", nil)
	if l.Len() != 0 || l.Match("anything.mkv") {
		t.Errorf("missing files must yield an empty list, got %v", l.entries)
	}
}

func TestMatch(t *testing.T) {
	l := List{entries: []string{"season 1", "x[y]z", "trailers"}}

	if !l.Match("/lib/Movies season 1/movie.mkv") {
		t.Error("directory substring must match")
	}
	if !l.Match("/lib/trailers/sample.mkv") {
		t.Error("file substring must match")
	}
	if !l.Match("/lib/a x[y]z b.mkv") {
		t.Error("glob-like entry must match literally")
	}
	if !l.Match("/lib/Movies season 11/movie.mkv") {
		t.Error("substring semantics: 'season 1' matches 'season 11' (documented mediatool caveat)")
	}
	if l.Match("/lib/Movies season 2/movie.mkv") {
		t.Error("must not match other seasons")
	}
}
