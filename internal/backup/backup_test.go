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

func TestSwapToDifferentDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.ts")
	newFile := filepath.Join(dir, "movie.mkv.tmp")
	dest := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}

	bak, err := SwapTo(src, newFile, dest)
	if err != nil {
		t.Fatalf("SwapTo: %v", err)
	}
	if bak != src+".old" {
		t.Errorf("backup path: got %s", bak)
	}
	if read(t, dest) != "converted" {
		t.Errorf("dest should hold the new content, got %q", read(t, dest))
	}
	if read(t, src+".old") != "original" {
		t.Errorf("backup should hold the original, got %q", read(t, src+".old"))
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("the source name must be gone (renamed to .old)")
	}
}

func TestSwapToRollsBack(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.ts")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := SwapTo(src, filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "movie.mkv")); err == nil {
		t.Fatal("expected error for missing new file")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original must be rolled back into place: %v", err)
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("rollback must remove the backup again")
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	newFile := filepath.Join(dir, "movie.mkv.tmp")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Replace(src, newFile); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if read(t, src) != "converted" {
		t.Errorf("src should hold the new content, got %q", read(t, src))
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("no backup must be created")
	}
	if _, err := os.Stat(newFile); err == nil {
		t.Error("the temp file must be gone (moved onto src)")
	}
}

func TestReplaceToDifferentDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.ts")
	newFile := filepath.Join(dir, "movie.mkv.tmp")
	dest := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("converted"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceTo(src, newFile, dest); err != nil {
		t.Fatalf("ReplaceTo: %v", err)
	}
	if read(t, dest) != "converted" {
		t.Errorf("dest should hold the new content, got %q", read(t, dest))
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("the original must be removed")
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("no backup must be created")
	}
}

func TestReplaceKeepsOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Replace(src, filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("expected error for missing new file")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original must stay untouched when the rename fails: %v", err)
	}
}

func TestSwapPair(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mkv")
	dub := filepath.Join(dir, "dub.mkv")
	newFile := filepath.Join(dir, "ref.mkv.tmp")
	for p, s := range map[string]string{ref: "reference", dub: "dubbed", newFile: "aligned"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bak, err := SwapPair(ref, dub, newFile, ref)
	if err != nil {
		t.Fatalf("SwapPair: %v", err)
	}
	if bak != ref+".old" {
		t.Errorf("backup path: got %s", bak)
	}
	if read(t, ref) != "aligned" {
		t.Errorf("reference should hold the aligned file, got %q", read(t, ref))
	}
	if read(t, ref+".old") != "reference" || read(t, dub+".old") != "dubbed" {
		t.Error("both originals must be preserved as .old")
	}
	if _, err := os.Stat(dub); err == nil {
		t.Error("the dub name must be gone (renamed to .old)")
	}
}

func TestSwapPairRefusesExistingDubBackup(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mkv")
	dub := filepath.Join(dir, "dub.mkv")
	newFile := filepath.Join(dir, "ref.mkv.tmp")
	for p, s := range map[string]string{ref: "reference", dub: "dubbed", newFile: "aligned", dub + ".old": "precious"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SwapPair(ref, dub, newFile, ref); err == nil {
		t.Fatal("expected an error when the dub backup already exists")
	}
	if read(t, ref) != "reference" || read(t, dub) != "dubbed" {
		t.Error("nothing must move when a backup exists")
	}
}

func TestSwapPairRollsBack(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mkv")
	dub := filepath.Join(dir, "dub.mkv")
	for p, s := range map[string]string{ref: "reference", dub: "dubbed"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SwapPair(ref, dub, filepath.Join(dir, "does-not-exist"), ref); err == nil {
		t.Fatal("expected an error for a missing aligned file")
	}
	if read(t, ref) != "reference" || read(t, dub) != "dubbed" {
		t.Error("both originals must be rolled back into place")
	}
	if _, err := os.Stat(ref + ".old"); err == nil {
		t.Error("rollback must remove the reference backup")
	}
	if _, err := os.Stat(dub + ".old"); err == nil {
		t.Error("rollback must remove the dub backup")
	}
}

func TestReplacePair(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mp4")
	dub := filepath.Join(dir, "dub.mkv")
	dest := filepath.Join(dir, "ref.mkv")
	newFile := filepath.Join(dir, "ref.tmp")
	for p, s := range map[string]string{ref: "reference", dub: "dubbed", newFile: "aligned"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReplacePair(ref, dub, newFile, dest); err != nil {
		t.Fatalf("ReplacePair: %v", err)
	}
	if read(t, dest) != "aligned" {
		t.Errorf("dest should hold the aligned file, got %q", read(t, dest))
	}
	for _, p := range []string{ref, dub, ref + ".old", dub + ".old"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s must not exist in -no-backup mode", p)
		}
	}
}
