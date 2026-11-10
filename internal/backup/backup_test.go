package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwap(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	newFile := filepath.Join(dir, "movie.mkv.tmp")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}

	bak, err := Swap(src, newFile)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if bak != src+".old" {
		t.Errorf("backup path: got %s", bak)
	}
	if read(t, src) != "converted" {
		t.Errorf("src should hold the new content, got %q", read(t, src))
	}
	if read(t, src+".old") != "original" {
		t.Errorf("backup should hold the original, got %q", read(t, src+".old"))
	}
}

func TestSwapRefusesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	newFile := filepath.Join(dir, "tmp")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+".old", []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Swap(src, newFile); err == nil {
		t.Fatal("expected error when the backup already exists")
	}
	if read(t, src) != "original" || read(t, src+".old") != "precious" {
		t.Error("swap must not touch anything when the backup exists")
	}
}

func TestSwapRollsBack(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Swap(src, filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("expected error for missing new file")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original must be rolled back into place: %v", err)
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("rollback must remove the backup again")
	}
}
