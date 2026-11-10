package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestCmdProgress(t *testing.T) {
	r := Cmd{}
	var times []time.Duration
	var speeds []string

	// Progress must not echo anything to the process stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = stderrW

	captured, err := r.Progress("sh",
		func(d time.Duration, s string) {
			times = append(times, d)
			speeds = append(speeds, s)
		},
		"-c", `printf 'frame= 1 fps=1 time=00:00:01.00 speed=1.0x   \r' >&2; printf 'frame= 2 fps=1 time=00:00:02.50 speed=1.5x   \r' >&2; echo real-error >&2`)

	os.Stderr = old
	stderrW.Close()
	var echoed bytes.Buffer
	echoed.ReadFrom(stderrR)
	stderrR.Close()

	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if len(times) != 2 || times[1] != 2500*time.Millisecond {
		t.Errorf("parsed times: %v", times)
	}
	if len(speeds) != 2 || speeds[1] != "1.5x" {
		t.Errorf("parsed speeds: %v", speeds)
	}
	if !strings.Contains(string(captured), "real-error") {
		t.Errorf("captured stderr: got %q", string(captured))
	}
	if echoed.Len() != 0 {
		t.Errorf("Progress must not echo stderr, got %q", echoed.String())
	}

	// non-zero exit still reports the captured stderr
	_, err = r.Progress("sh", func(time.Duration, string) {}, "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("exit error must contain stderr, got %v", err)
	}
}

// streamRunner is a Runner without the Progress capability.
type streamRunner struct{ Cmd }

func TestStreamProgressFallback(t *testing.T) {
	var called bool
	captured, err := StreamProgress(streamRunner{}, "sh", func(time.Duration, string) {
		called = true
	}, "-c", "echo hi >&2")
	if err != nil {
		t.Fatalf("StreamProgress: %v", err)
	}
	if called {
		t.Error("fallback must not call the stats callback")
	}
	if !strings.Contains(string(captured), "hi") {
		t.Errorf("captured stderr: got %q", string(captured))
	}

	captured, err = StreamProgress(Cmd{}, "sh", nil, "-c", "echo parsed >&2")
	if err != nil || !strings.Contains(string(captured), "parsed") {
		t.Errorf("nil callback must fall back to Stream: %q, %v", string(captured), err)
	}
}

func TestLastErrLine(t *testing.T) {
	out := []byte("frame= 1 fps=1 time=00:00:01.00 speed=1.0x   \r" +
		"[matroska] bad header\r\n" +
		"frame= 2 fps=1 time=00:00:02.00 speed=1.0x   \r" +
		"Lsize=   123kB time=00:00:02.00 bitrate= 100.\n")
	if got := LastErrLine(out); got != "[matroska] bad header" {
		t.Errorf("got %q", got)
	}
	if got := LastErrLine([]byte("frame= 1 time=00:00:01.00\r")); got != "" {
		t.Errorf("stats-only output must give an empty line, got %q", got)
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
