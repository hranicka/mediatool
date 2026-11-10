package walk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMedia(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "season 1")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{
		"a.mkv",
		"c.mp4",
		filepath.Join("season 1", "b.MKV"),
		filepath.Join("season 1", "._hidden.mkv"), // AppleDouble - excluded
		"movie.mkv.old",                    // backup - excluded
		"notes.txt",                        // not media - excluded
		filepath.Join("season 1", "e.mov"), // other container - excluded
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Media(dir)
	if err != nil {
		t.Fatalf("Media: %v", err)
	}
	want := []string{
		filepath.Join(dir, "a.mkv"),
		filepath.Join(dir, "c.mp4"),
		filepath.Join(sub, "b.MKV"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMediaMissingDir(t *testing.T) {
	if _, err := Media(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing directory")
	}
}
