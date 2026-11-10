package walk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsMedia(t *testing.T) {
	for _, ext := range Extensions {
		if !IsMedia(ext) {
			t.Errorf("IsMedia(%q) must be true", ext)
		}
		if !IsMedia(strings.ToUpper(ext)) {
			t.Errorf("IsMedia(%q) must be case-insensitive", ext)
		}
	}
	for _, ext := range []string{"", ".txt", ".nfo", ".jpg"} {
		if IsMedia(ext) {
			t.Errorf("IsMedia(%q) must be false", ext)
		}
	}
}

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
		"movie.mkv.old",  // backup - excluded
		"notes.txt",      // not media - excluded
		"clip.mts",       // camera format - picked up
		"cam.AVI",        // camera format, case-insensitive
		"movie.m2ts.old", // camera-format backup - excluded
		"e.txt.mts",      // media extension wins over the stem
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
		filepath.Join(dir, "cam.AVI"),
		filepath.Join(dir, "clip.mts"),
		filepath.Join(dir, "e.txt.mts"),
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

func TestMediaSkipsUnreadableSubdir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "locked")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	okFile := filepath.Join(dir, "ok.mkv")
	if err := os.WriteFile(okFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	got, err := Media(dir)
	if err != nil {
		t.Fatalf("unreadable subdirectory must not abort the scan: %v", err)
	}
	if len(got) != 1 || got[0] != okFile {
		t.Errorf("got %v, want [%s]", got, okFile)
	}
}
