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
	write(t, filepath.Join(dir, ".mediatool-ignore"), "trailers\n# a comment\n\nsub/\r\n")
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

func TestLoadAncestors(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "foo", "bar", "baz")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(base, ".mediatool-ignore"), "grand\n")
	write(t, filepath.Join(base, "foo", "bar", ".cleaner-ignore"), "bar-level\n")
	write(t, filepath.Join(child, ".mediatool-ignore"), "child-level\n")

	l := Load(child, "clean", nil)
	for _, want := range []string{"grand", "bar-level", "child-level"} {
		if !l.Match("/some/" + want + "/movie.mkv") {
			t.Errorf("ancestor entry %q missing: %v", want, l.entries)
		}
	}

	// the command's own file is looked up at every level too
	write(t, filepath.Join(base, "foo", ".hevcconverter-ignore"), "foo-tool\n")
	l2 := Load(child, "convert", nil)
	if !l2.Match("/x/foo-tool/y.mkv") {
		t.Errorf("ancestor command file missing: %v", l2.entries)
	}
	if l2.Match("/x/bar-level/y.mkv") {
		t.Error("the cleaner file must not apply to the convert subcommand")
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
