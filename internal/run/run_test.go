package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdOutput(t *testing.T) {
	r := Cmd{}
	out, err := r.Output("sh", "-c", "echo stdout-line; echo stderr-line >&2")
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if strings.TrimSpace(string(out)) != "stdout-line" {
		t.Errorf("stdout: got %q", string(out))
	}

	_, err = r.Output("sh", "-c", "echo boom >&2; exit 3")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error must contain stderr, got %v", err)
	}
}

func TestCmdStream(t *testing.T) {
	r := Cmd{}
	captured, err := r.Stream("sh", "-c", "echo captured-stderr >&2")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if strings.TrimSpace(string(captured)) != "captured-stderr" {
		t.Errorf("captured stderr: got %q", string(captured))
	}
}

func TestLocateEnvOverride(t *testing.T) {
	dir := t.TempDir()
	fb := filepath.Join(dir, "ffmpeg")
	pb := filepath.Join(dir, "ffprobe")
	for _, p := range []string{fb, pb} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FFMPEG_BIN", fb)

	// without FFPROBE_BIN, the sibling ffprobe of the chosen ffmpeg wins
	ffmpeg, ffprobe, err := Locate()
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if ffmpeg != fb {
		t.Errorf("ffmpeg: got %s, want %s", ffmpeg, fb)
	}
	if ffprobe != pb {
		t.Errorf("ffprobe: got %s, want sibling %s", ffprobe, pb)
	}
}

func TestLocateSiblingProbe(t *testing.T) {
	dir := t.TempDir()
	fb := filepath.Join(dir, "ffmpeg")
	pb := filepath.Join(dir, "ffprobe")
	for _, p := range []string{fb, pb} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FFMPEG_BIN", fb)
	t.Setenv("FFPROBE_BIN", "")

	_, ffprobe, err := Locate()
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if ffprobe != pb {
		t.Errorf("ffprobe: got %s, want sibling %s", ffprobe, pb)
	}
}

func TestLocateInvalidOverride(t *testing.T) {
	t.Setenv("FFMPEG_BIN", filepath.Join(t.TempDir(), "missing"))
	if _, _, err := Locate(); err == nil {
		t.Fatal("expected error for a missing FFMPEG_BIN")
	}
}

func TestTmpGuard(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.tmp")
	if err := os.WriteFile(gone, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add followed by Remove deregisters the path again
	g := &TmpGuard{}
	g.Add(gone)
	g.Remove(gone)
	g.Cleanup()
	if _, err := os.Stat(gone); err != nil {
		t.Error("deregistered path must survive cleanup")
	}

	g.Add(gone)
	g.Cleanup()
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Error("registered path must be removed by cleanup")
	}
}
