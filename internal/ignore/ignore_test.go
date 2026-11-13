package ignore

import (
	"os"
	"path/filepath"
	"strings"
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

	l, warnings := Load(dir, "clean", []string{"CLI", "extra"})
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
	// .cleaner-ignore is a legacy name: still honored, but must warn
	if len(warnings) != 1 || !strings.Contains(warnings[0], ".cleaner-ignore") ||
		!strings.Contains(warnings[0], ".mediatool-clean-ignore") {
		t.Errorf("legacy rename warning missing: %v", warnings)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	l, warnings := Load(t.TempDir(), "dup", nil)
	if len(warnings) != 0 {
		t.Errorf("no files must yield no warnings: %v", warnings)
	}
	if l.Len() != 0 || l.Match("anything.mkv") {
		t.Errorf("missing files must yield an empty list, got %v", l.entries)
	}
}

func TestDupHasNoPerCommandFile(t *testing.T) {
	// dup is a read-only report: only the shared file and -i apply
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".mediatool-dup-ignore"), "dupfile\n")
	write(t, filepath.Join(dir, ".dupfinder-ignore"), "duplegacy\n")

	l, warnings := Load(dir, "dup", nil)
	if l.Match("/x/dupfile/y.mkv") || l.Match("/x/duplegacy/y.mkv") {
		t.Errorf("dup must not have per-command ignore files: %v", l.entries)
	}
	if len(warnings) != 0 {
		t.Errorf("no deprecation warnings expected for dup: %v", warnings)
	}

	// the shared file still governs dup
	write(t, filepath.Join(dir, ".mediatool-ignore"), "shared\n")
	l2, _ := Load(dir, "dup", nil)
	if !l2.Match("/x/shared/y.mkv") {
		t.Errorf("shared file must apply to dup: %v", l2.entries)
	}
}

func TestLoadAncestors(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "foo", "bar", "baz")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(base, ".mediatool-ignore"), "grand\n")
	write(t, filepath.Join(base, "foo", "bar", ".mediatool-clean-ignore"), "bar-level\n")
	write(t, filepath.Join(child, ".mediatool-ignore"), "child-level\n")

	l, warnings := Load(child, "clean", nil)
	if len(warnings) != 0 {
		t.Errorf("new-name files must not warn: %v", warnings)
	}
	for _, want := range []string{"grand", "bar-level", "child-level"} {
		if !l.Match("/some/" + want + "/movie.mkv") {
			t.Errorf("ancestor entry %q missing: %v", want, l.entries)
		}
	}

	// the command's file is looked up at every level too (legacy name: warns)
	write(t, filepath.Join(base, "foo", ".hevcconverter-ignore"), "foo-tool\n")
	l2, warnings2 := Load(child, "convert", nil)
	if !l2.Match("/x/foo-tool/y.mkv") {
		t.Errorf("ancestor command file missing: %v", l2.entries)
	}
	if len(warnings2) != 1 || !strings.Contains(warnings2[0], ".mediatool-convert-ignore") {
		t.Errorf("legacy rename warning missing: %v", warnings2)
	}
	if l2.Match("/x/bar-level/y.mkv") {
		t.Error("the cleaner file must not apply to the convert subcommand")
	}
}

func TestLegacyAndNewFilesBothApply(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".cleaner-ignore"), "old\n")
	write(t, filepath.Join(dir, ".mediatool-clean-ignore"), "new\n")

	l, warnings := Load(dir, "clean", nil)
	if !l.Match("/x/old/y.mkv") || !l.Match("/x/new/y.mkv") {
		t.Errorf("both the legacy and the new file must apply: %v", l.entries)
	}
	if len(warnings) != 1 {
		t.Errorf("still exactly one warning for the legacy file: %v", warnings)
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

func TestMerge(t *testing.T) {
	a := List{entries: []string{"one", "two"}}
	b := List{entries: []string{"two", "three"}}
	got := a.Merge(b).entries
	want := []string{"one", "two", "three"}
	if len(got) != len(want) {
		t.Fatalf("entries: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries: got %v, want %v", got, want)
		}
	}
}
