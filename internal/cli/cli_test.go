package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIgnoresZeroValueString(t *testing.T) {
	// the flag package calls String() on a reflectively constructed zero
	// value when rendering -h output; it must not panic
	var v Ignores
	if got := v.String(); got != "" {
		t.Errorf("zero value: got %q, want empty", got)
	}
	if err := v.Set("a,b"); err != nil {
		t.Errorf("zero value Set: %v", err)
	}
}

func TestIgnoresSet(t *testing.T) {
	var dst []string
	v := Ignores{Dst: &dst}
	if err := v.Set("a,,b,"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(dst, ",") != "a,b" {
		t.Errorf("got %v, want [a b]", dst)
	}
	if v.String() != "a,b" {
		t.Errorf("String: got %q", v.String())
	}
}

func TestResolveMediaTarget(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveMediaTarget(dir, false)
	if err != nil || got != dir {
		t.Errorf("directory: %v %v", got, err)
	}

	if _, err := ResolveMediaTarget(filepath.Join(dir, "missing"), false); err == nil {
		t.Error("missing target must fail")
	}

	f := filepath.Join(dir, "movie.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMediaTarget(f, true); err == nil {
		t.Error("non-media file must fail")
	}
	if _, err := ResolveMediaTarget(f, false); err == nil {
		t.Error("file target must fail when files are not allowed")
	}

	mkv := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(mkv, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveMediaTarget(mkv, true); err != nil || got != mkv {
		t.Errorf("media file: %v %v", got, err)
	}
}

func TestIgnoresDir(t *testing.T) {
	dir := t.TempDir()
	if got := IgnoresDir(dir); got != dir {
		t.Errorf("directory target: got %s", got)
	}
	f := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := IgnoresDir(f); got != dir {
		t.Errorf("file target: got %s, want %s", got, dir)
	}
}
