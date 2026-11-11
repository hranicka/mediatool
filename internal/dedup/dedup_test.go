package dedup

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

// patternFrame fills one vhash.Side x vhash.Side gray frame with
// deterministic pseudo-random content seeded by the frame index: frame t
// is always the same pixels, so the same content produces the same hash
// sequence (possibly shifted), while different frames hash far apart.
func patternFrame(t int) []byte {
	rng := rand.New(rand.NewSource(int64(t)))
	frame := make([]byte, vhash.FrameLen)
	rng.Read(frame)
	return frame
}

// framesAt builds the raw stream of count frames starting at frame index
// start (frame k of the stream is pattern frame start+k).
func framesAt(start, count int) []byte {
	buf := make([]byte, 0, count*vhash.FrameLen)
	for t := start; t < start+count; t++ {
		buf = append(buf, patternFrame(t)...)
	}
	return buf
}

// noiseFrames produces per-frame pseudo-random gray data, i.e. content no
// other file shares.
func noiseFrames(seed, count int) []byte {
	rng := rand.New(rand.NewSource(int64(seed)))
	buf := make([]byte, count*vhash.FrameLen)
	rng.Read(buf)
	return buf
}

type fakeFF struct {
	probeFor  func(src string) (string, bool)
	framesFor func(src string) []byte
	frameArgs [][]string
}

func (f *fakeFF) Output(name string, args ...string) ([]byte, error) {
	if name == "ffprobe" {
		src := args[len(args)-1]
		if f.probeFor != nil {
			if json, ok := f.probeFor(src); ok {
				return []byte(json), nil
			}
		}
		return []byte(`{"streams":[],"format":{}}`), nil
	}
	if name == "ffmpeg" {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-i" {
				f.frameArgs = append(f.frameArgs, args)
				if f.framesFor != nil {
					return f.framesFor(args[i+1]), nil
				}
			}
		}
		return []byte{}, nil
	}
	return []byte{}, nil
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	return nil, nil
}

func videoJSON(codec string, w, h int, dur string) string {
	return fmt.Sprintf(`{"streams":[{"index":0,"codec_name":"%s","codec_type":"video","width":%d,"height":%d,"disposition":{"attached_pic":0}}],"format":{"duration":"%s","size":"1000","bit_rate":"1"}}`, codec, w, h, dur)
}

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

func TestReportSelfSimilarNote(t *testing.T) {
	items := []item{
		{path: "/x/a.mkv", size: 1000, width: 1920, height: 1080, codec: "h264", duration: 100, hasDur: true, bg: 60},
		{path: "/x/b.mp4", size: 800, width: 1920, height: 1080, codec: "h264", duration: 100, hasDur: true, bg: 500},
	}

	out := &bytes.Buffer{}
	tl := &tool{rep: ui.New(out, ui.Profile{})}
	tl.report(items, [][]int{{0, 1}}, 0)
	if !strings.Contains(out.String(), "highly self-similar (background median 60") {
		t.Errorf("self-similar group must carry a caution note: %s", out.String())
	}

	// content-rich members: no note
	items[0].bg = 500
	out.Reset()
	tl.report(items, [][]int{{0, 1}}, 0)
	if strings.Contains(out.String(), "self-similar") {
		t.Errorf("rich-content group must not warn: %s", out.String())
	}
}

func TestDedupFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"-interval", "0", "dir"},
		{"-min-match", "0", "dir"},
		{"-min-match", "1.2", "dir"},
		{"-max-dist", "0", "dir"},
		{"-max-dist", "1.5", "dir"},
	} {
		if err := Run(args); err == nil {
			t.Errorf("Run(%v) must fail validation", args)
		}
	}
}

func TestCandidateIndicesWithMissingDuration(t *testing.T) {
	items := []item{
		{path: "a", duration: 100, hasDur: true},
		{path: "b", duration: 300, hasDur: true}, // incompatible with a
		{path: "c", hasDur: false},               // no duration: pairs with everything
	}
	got := candidateIndices(items, 5)
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("got %v, want [0 1 2] (the duration-less item drags all into extraction)", got)
	}
}

func TestDedupNoDurationNote(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mkv")
	b := filepath.Join(dir, "b.mkv")
	writeFile(t, a, 100)
	writeFile(t, b, 100)

	noDur := `{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":320,"height":240,"disposition":{"attached_pic":0}}],"format":{"size":"1000","bit_rate":"1"}}`
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if filepath.Base(src) == "a.mkv" {
			return noDur, true
		}
		return videoJSON("h264", 320, 240, "200.0"), true
	}, framesFor: func(src string) []byte {
		return framesAt(0, 10)
	}}
	out := &bytes.Buffer{}
	tl := &tool{
		r: ff, ffmpegBin: "ffmpeg", ffprobeBin: "ffprobe",
		cfg:     &Config{Dir: dir, Interval: 2, MinMatch: 0.8, MaxDist: 0.15, Tolerance: 5},
		ignores: mustIgnore(dir, "dedup", nil),
		rep:     ui.New(out, ui.Profile{}),
	}
	// the no-duration note goes to stderr (rep.Note); capture it via a pipe
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = errW
	runErr := tl.runAll()
	errW.Close()
	os.Stderr = oldStderr
	note, _ := io.ReadAll(errR)
	if runErr != nil {
		t.Fatalf("runAll: %v (out: %s)", runErr, out.String())
	}
	if !strings.Contains(string(note), "have no container duration") {
		t.Errorf("missing no-duration note on stderr: %s", note)
	}
	// the duration-less file pairs with everything: both get extracted
	if len(ff.frameArgs) != 2 {
		t.Errorf("both files must be hashed, got %d extractions", len(ff.frameArgs))
	}
}

func TestDedupSkipsIncompatiblePairs(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "a.mkv"), // 100 s
		filepath.Join(dir, "b.mkv"), // 100 s - same content as a
		filepath.Join(dir, "c.mkv"), // 200 s - same frames, out of tolerance for a/b
	}
	for _, p := range paths {
		writeFile(t, p, 500)
	}
	durs := []string{"100.0", "100.0", "200.0"}
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		base := filepath.Base(src)
		for i, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
			if base == name {
				return videoJSON("h264", 320, 240, durs[i]), true
			}
		}
		return "", false
	}, framesFor: func(src string) []byte {
		return framesAt(0, 50) // all three carry identical content
	}}
	out := &bytes.Buffer{}
	tl := &tool{
		r: ff, ffmpegBin: "ffmpeg", ffprobeBin: "ffprobe",
		cfg:     &Config{Dir: dir, Interval: 2, MinMatch: 0.8, MaxDist: 0.15, Tolerance: 5},
		ignores: mustIgnore(dir, "dedup", nil),
		rep:     ui.New(out, ui.Profile{}),
	}
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Duplicate content group #1: 2 files") {
		t.Errorf("a and b must group, c must stay out: %s", got)
	}
	if strings.Contains(got, "dup  "+paths[2]) {
		t.Errorf("the out-of-tolerance c must not be grouped: %s", got)
	}
	if len(ff.frameArgs) != 2 {
		t.Errorf("only a and b may be hashed, got %d extractions", len(ff.frameArgs))
	}
}

func TestReportGroupOrderDeterministic(t *testing.T) {
	items := []item{
		{path: "/x/b1.mkv", size: 1000, width: 100, height: 100, codec: "h264"},
		{path: "/x/a1.mkv", size: 900, width: 100, height: 100, codec: "h264"},
		{path: "/x/d2.mkv", size: 1000, width: 100, height: 100, codec: "h264"},
		{path: "/x/c2.mkv", size: 900, width: 100, height: 100, codec: "h264"},
	}
	// two equal-saving groups: the tie-break (keeper path) must keep the
	// report stable regardless of the union-find map order
	var first string
	for run := 0; run < 50; run++ {
		groups := unionGroups(4, [][2]int{{0, 1}, {2, 3}})
		out := &bytes.Buffer{}
		tl := &tool{rep: ui.New(out, ui.Profile{})}
		tl.report(items, groups, 0)
		if run == 0 {
			first = out.String()
			continue
		}
		if out.String() != first {
			t.Fatal("group order is not deterministic across runs")
		}
	}
	if !strings.Contains(first, "keep /x/b1.mkv") || strings.Index(first, "/x/b1.mkv") > strings.Index(first, "/x/d2.mkv") {
		t.Errorf("groups must be ordered by keeper path on ties: %s", first)
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
