package dedup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/ui"
)

// mustIgnore loads the ignore list, dropping the deprecation warnings
// (only the ignore package's own tests assert those).
func mustIgnore(dir, tool string, extra []string) ignore.List {
	l, _ := ignore.Load(dir, tool, extra)
	return l
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDedupRunAll pins the full pipeline: two encodes of the same content
// (one shifted) form a group with the keeper suggestion, unrelated and
// out-of-tolerance files stay out, and a video-less file is skipped.
func TestDedupRunAll(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "movie-a.mkv") // the content, 200 s
	b := filepath.Join(dir, "movie-b.mp4") // same content, shifted, 194 s
	c := filepath.Join(dir, "unrelated.mkv")
	d := filepath.Join(dir, "audio-only.mkv")
	writeFile(t, a, 2_000)
	writeFile(t, b, 1_000)
	writeFile(t, c, 5_000)
	writeFile(t, d, 10)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		switch filepath.Base(src) {
		case "movie-a.mkv":
			return videoJSON("h264", 1920, 1080, "200.0"), true
		case "movie-b.mp4":
			return videoJSON("hevc", 1920, 1080, "194.0"), true
		case "unrelated.mkv":
			return videoJSON("h264", 1280, 720, "400.0"), true
		default:
			return `{"streams":[{"index":0,"codec_name":"mp3","codec_type":"audio"}],"format":{"duration":"100.0"}}`, true
		}
	}, framesFor: func(src string) []byte {
		switch filepath.Base(src) {
		case "movie-a.mkv":
			return framesAt(0, 100) // 200 s at 2 s interval
		case "movie-b.mp4":
			return framesAt(3, 97) // same content, 6 s (3 frames) later
		case "unrelated.mkv":
			return noiseFrames(77, 200)
		default:
			return nil
		}
	}}

	out := &bytes.Buffer{}
	tl := &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		cfg:        &Config{Dir: dir, Interval: 2, MinMatch: 0.8, MaxDist: 0.15, Tolerance: 5},
		ignores:    mustIgnore(dir, "dedup", nil),
		rep:        ui.New(out, ui.Profile{}),
	}
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Duplicate content group #1: 2 files") {
		t.Errorf("missing group header: %s", got)
	}
	if !strings.Contains(got, "keep "+a) || !strings.Contains(got, "dup  "+b) {
		t.Errorf("missing group members with tags (keeper first): %s", got)
	}
	if !strings.Contains(got, "(1920x1080 h264") || !strings.Contains(got, "(1920x1080 hevc") {
		t.Errorf("missing member metadata: %s", got)
	}
	if strings.Contains(got, "unrelated.mkv") {
		t.Errorf("the out-of-tolerance file must not appear at all: %s", got)
	}
	if !strings.Contains(got, "Skipping (no real video stream): "+d) {
		t.Errorf("the video-less file must be skipped: %s", got)
	}
	if !strings.Contains(got, "Potential savings: "+media.HumanBytes(1000)+" in 1 group(s)") {
		t.Errorf("missing savings line: %s", got)
	}
	if !strings.Contains(got, "Done. Duplicate content groups: 1.") {
		t.Errorf("missing summary: %s", got)
	}
}

func TestDedupRunAllIgnores(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "movie.mkv")
	writeFile(t, a, 100)

	ff := &fakeFF{probeFor: func(string) (string, bool) {
		return videoJSON("h264", 1920, 1080, "200.0"), true
	}}
	out := &bytes.Buffer{}
	tl := &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		cfg:        &Config{Dir: dir, Interval: 2, MinMatch: 0.8, MaxDist: 0.15, Tolerance: 5, Ignores: []string{"movie"}},
		ignores:    mustIgnore(dir, "dedup", []string{"movie"}),
		rep:        ui.New(out, ui.Profile{}),
	}
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}
	if !strings.Contains(out.String(), "Skipping (ignored): "+a) {
		t.Errorf("ignored file must be skipped: %s", out.String())
	}
	if len(ff.frameArgs) != 0 {
		t.Errorf("ignored files must not be hashed: %v", ff.frameArgs)
	}
}

func TestDedupRunAllGroupsThreeVersions(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "x.mkv"),          // the content, big
		filepath.Join(dir, "x-reencode.mp4"), // smaller encode
		filepath.Join(dir, "x-old.avi"),      // smallest, worst
	}
	for i, p := range paths {
		writeFile(t, p, 3_000-1_000*i)
	}

	ff := &fakeFF{probeFor: func(string) (string, bool) {
		return videoJSON("h264", 1920, 1080, "200.0"), true
	}, framesFor: func(string) []byte {
		return framesAt(0, 50) // all three carry the identical content
	}}

	out := &bytes.Buffer{}
	tl := &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		cfg:        &Config{Dir: dir, Interval: 2, MinMatch: 0.8, MaxDist: 0.15, Tolerance: 5},
		ignores:    mustIgnore(dir, "dedup", nil),
		rep:        ui.New(out, ui.Profile{}),
	}
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Duplicate content group #1: 3 files, "+media.HumanBytes(6000)+" ("+media.HumanBytes(3000)+" reclaimable)") {
		t.Errorf("missing three-member group header: %s", got)
	}
	// members ordered by size: the keeper first
	if !strings.Contains(got, "keep "+paths[0]) || !strings.Contains(got, "dup  "+paths[2]) {
		t.Errorf("wrong member order: %s", got)
	}
	if !strings.Contains(got, "Done. Duplicate content groups: 1.") {
		t.Errorf("missing summary: %s", got)
	}
}
