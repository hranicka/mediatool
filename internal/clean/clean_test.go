package clean

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
)

// fakeFF fakes both binaries; see convert_test.go for the pattern.
type fakeFF struct {
	probeFor  func(src string) (string, bool)
	streamErr error
	remuxSize int64
	remuxLog  [][]string
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
	return []byte{}, nil
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	f.remuxLog = append(f.remuxLog, args)
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	if err := os.WriteFile(args[len(args)-1], make([]byte, f.remuxSize), 0o644); err != nil {
		return nil, err
	}
	return nil, nil
}

func streamJSON(codec, ctype, lang, extra string) string {
	parts := []string{
		`"index":0`,
		fmt.Sprintf(`"codec_name":"%s"`, codec),
		fmt.Sprintf(`"codec_type":"%s"`, ctype),
		`"disposition":{"attached_pic":0}`,
	}
	if lang != "" {
		parts = append(parts, fmt.Sprintf(`"tags":{"language":"%s"}`, lang))
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func coverJSON() string {
	return `{"index":1,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}}`
}

// libraryJSON builds the cs/en library fixture: cs+ger audio, eng/ger/cze
// subs, cover art.
func libraryJSON() string {
	streams := []string{
		streamJSON("h264", "video", "", ""),
		coverJSON(),
		streamJSON("ac3", "audio", "ces", ""),
		streamJSON("dts", "audio", "ger", ""),
		streamJSON("subrip", "subtitle", "eng", ""),
		streamJSON("subrip", "subtitle", "ger", ""),
		streamJSON("subrip", "subtitle", "cze", ""),
	}
	return fmt.Sprintf(`{"streams":[%s],"format":{"duration":"600.0","size":"1000000","bit_rate":"8000000"}}`, strings.Join(streams, ","))
}

// remuxJSON produces the expected post-remux probe: video 1, audio a, subs s.
func remuxJSON(a, s int) string {
	var streams []string
	streams = append(streams, streamJSON("h264", "video", "", ""))
	for i := 0; i < a; i++ {
		streams = append(streams, streamJSON("ac3", "audio", "ces", ""))
	}
	for i := 0; i < s; i++ {
		streams = append(streams, streamJSON("subrip", "subtitle", "eng", ""))
	}
	return fmt.Sprintf(`{"streams":[%s],"format":{"duration":"600.0","size":"1","bit_rate":"1"}}`, strings.Join(streams, ","))
}

func newTestTool(t *testing.T, dir string, ff run.Runner, cfg *Config, out *bytes.Buffer) *tool {
	t.Helper()
	return &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		cfg:        cfg,
		whitelist:  mediaWhitelist(cfg.Langs),
		ignores:    mustIgnore(dir, "clean", cfg.Ignores),
		rep:        ui.New(out, ui.Profile{}),
		guard:      &run.TmpGuard{},
	}
}

func mediaWhitelist(langs string) []string {
	if langs == "" {
		langs = "cs,en"
	}
	return media.ExpandWhitelist(strings.Split(langs, ","))
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// mustIgnore loads the ignore list, dropping the deprecation warnings
// (only the ignore package's own tests assert those).
func mustIgnore(dir, tool string, extra []string) ignore.List {
	l, _ := ignore.Load(dir, tool, extra)
	return l
}

func TestCleanHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(1, 2), true
		}
		return libraryJSON(), true
	}, remuxSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	if tl.cleaned != 1 || tl.failed != 0 {
		t.Fatalf("counters: cleaned=%d failed=%d out=%s", tl.cleaned, tl.failed, out.String())
	}
	args := ff.remuxLog[0]
	for _, pair := range [][2]string{
		{"-map", "0:v"}, {"-map", "0:a?"}, {"-map", "0:s?"},
		{"-map", "-0:v:1"}, // cover art
		{"-map", "-0:a:1"}, // ger dts
		{"-map", "-0:s:1"}, // ger subs
		{"-c", "copy"}, {"-map_metadata:g", "0:g"}, {"-f", "matroska"},
	} {
		if !hasPair(args, pair[0], pair[1]) {
			t.Errorf("remux args missing %q: %v", pair, args)
		}
	}
	if _, err := os.Stat(src + ".old"); err != nil {
		t.Error("backup missing")
	}
}

func TestCleanNoBackup(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(1, 2), true
		}
		return libraryJSON(), true
	}, remuxSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en", NoBackup: true}, out)
	tl.processFile(1, 1, src)

	if tl.cleaned != 1 || tl.failed != 0 {
		t.Fatalf("counters: cleaned=%d failed=%d out=%s", tl.cleaned, tl.failed, out.String())
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("-no-backup must not create a backup")
	}
	st, err := os.Stat(src)
	if err != nil {
		t.Fatalf("replacement missing: %v", err)
	}
	if st.Size() != 500_000 {
		t.Errorf("original not replaced in place: size %d, want 500000", st.Size())
	}
}

func TestCleanOldRemovesBackups(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old1 := filepath.Join(dir, "movie.mkv.old")
	old2 := filepath.Join(sub, "show.ts.old")
	keep := filepath.Join(dir, "other.mkv")
	stranger := filepath.Join(dir, "notes.old.bak")
	writeFile(t, old1, 1_500_000)
	writeFile(t, old2, 500_000)
	writeFile(t, keep, 10)
	writeFile(t, stranger, 10)

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, &fakeFF{}, &Config{Target: dir, RemoveOld: true}, out)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if tl.cleaned != 2 || tl.failed != 0 {
		t.Fatalf("counters: cleaned=%d failed=%d out=%s", tl.cleaned, tl.failed, out.String())
	}
	if tl.bytesIn != 2_000_000 {
		t.Errorf("freed bytes: got %d, want 2000000", tl.bytesIn)
	}
	for _, gone := range []string{old1, old2} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s must be removed", gone)
		}
	}
	for _, kept := range []string{keep, stranger} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s must stay", kept)
		}
	}
	got := out.String()
	if !strings.Contains(got, "Removed: "+old1) || !strings.Contains(got, "Removed: "+old2) {
		t.Errorf("missing removal lines: %s", got)
	}
	if !strings.Contains(got, "freed") {
		t.Errorf("summary must report the freed bytes: %s", got)
	}
}

func TestCleanOldDryRunAndIgnores(t *testing.T) {
	dir := t.TempDir()
	old1 := filepath.Join(dir, "movie.mkv.old")
	old2 := filepath.Join(dir, "show.ts.old")
	writeFile(t, old1, 1_000)
	writeFile(t, old2, 2_000)

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, &fakeFF{}, &Config{Target: dir, RemoveOld: true, DryRun: true, Ignores: []string{"show"}}, out)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if tl.cleaned != 1 || tl.untouched != 1 || tl.failed != 0 {
		t.Fatalf("counters: cleaned=%d untouched=%d out=%s", tl.cleaned, tl.untouched, out.String())
	}
	for _, f := range []string{old1, old2} {
		if _, err := os.Stat(f); err != nil {
			t.Error("dry run must not delete")
		}
	}
	got := out.String()
	if !strings.Contains(got, "Would remove: "+old1) {
		t.Errorf("missing dry-run line: %s", got)
	}
	if !strings.Contains(got, "Skipping (ignored): "+old2) {
		t.Errorf("missing ignore line: %s", got)
	}
}

func TestCleanOldRefusesNonBackupFile(t *testing.T) {
	dir := t.TempDir()
	notOld := filepath.Join(dir, "movie.mkv")
	writeFile(t, notOld, 10)

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, &fakeFF{}, &Config{Target: notOld, RemoveOld: true}, out)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}
	if tl.cleaned != 0 || tl.untouched != 1 || tl.failed != 0 {
		t.Fatalf("counters: cleaned=%d untouched=%d out=%s", tl.cleaned, tl.untouched, out.String())
	}
	if _, err := os.Stat(notOld); err != nil {
		t.Error("a non-backup file must never be removed")
	}
	if !strings.Contains(out.String(), "not a .old file") {
		t.Errorf("missing refusal line: %s", out.String())
	}
}

func TestCleanDryRunPlan(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(string) (string, bool) { return libraryJSON(), true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en", DryRun: true}, out)
	tl.processFile(1, 1, src)

	if tl.cleaned != 1 || len(ff.remuxLog) != 0 {
		t.Fatalf("dry run must not remux: %+v", tl)
	}
	for _, want := range []string{
		"Would clean",
		"remove cover art video #1 (mjpeg)",
		"remove audio #1 (dts, lang 'ger')",
		"remove subtitle #1 (subrip, lang 'ger')",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan missing %q: %s", want, out.String())
		}
	}
}

func TestCleanSoleTrackRule(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "solo.mkv")
	writeFile(t, src, 1000)

	soloJSON := fmt.Sprintf(`{"streams":[%s,%s,%s],"format":{"duration":"600.0","size":"1000","bit_rate":"1"}}`,
		streamJSON("h264", "video", "", ""),
		streamJSON("aac", "audio", "rus", ""),
		streamJSON("subrip", "subtitle", "rus", ""))

	// default: the lone foreign tracks survive
	ff := &fakeFF{probeFor: func(string) (string, bool) { return soloJSON, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)
	if tl.untouched != 1 || len(ff.remuxLog) != 0 {
		t.Errorf("sole tracks must be kept: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "OK (nothing to remove)") {
		t.Errorf("unexpected output: %s", out.String())
	}
	// the kept foreign sole tracks are surfaced as notices, not hidden
	for _, want := range []string{"sole audio track", "sole subtitle track"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("sole-track notice %q missing: %s", want, out.String())
		}
	}

	// -e purges them
	ff2 := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(0, 0), true
		}
		return soloJSON, true
	}, remuxSize: 1}
	tl2 := newTestTool(t, dir, ff2, &Config{Target: dir, Langs: "cs,en", PurgeSole: true}, out)
	tl2.processFile(1, 1, src)
	if tl2.cleaned != 1 {
		t.Errorf("purge must remove sole tracks: %+v", tl2)
	}
	args := ff2.remuxLog[0]
	if !hasPair(args, "-map", "-0:a:0") || !hasPair(args, "-map", "-0:s:0") {
		t.Errorf("purge maps missing: %v", args)
	}
}

// TestCleanSoleTrackNoticeQuiet pins that the sole-track keep notice stays
// a permanent yellow line in the default (quiet) interactive rendering,
// even though the file itself is an untouched "nothing to remove" no-op.
func TestCleanSoleTrackNoticeQuiet(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "solo.mkv")
	writeFile(t, src, 1000)

	soloJSON := fmt.Sprintf(`{"streams":[%s,%s],"format":{"duration":"600.0","size":"1000","bit_rate":"1"}}`,
		streamJSON("h264", "video", "", ""),
		streamJSON("aac", "audio", "rus", ""))
	ff := &fakeFF{probeFor: func(string) (string, bool) { return soloJSON, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.rep = ui.New(out, ui.Profile{Interactive: true, Color: true, UTF8: true, Width: 100})
	tl.processFile(1, 1, src)

	got := out.String()
	if !strings.Contains(got, "\x1b[33m") || !strings.Contains(got, "sole audio track") {
		t.Errorf("quiet run must show a yellow sole-track notice: %q", got)
	}
	// the notice is a permanent line (newline-terminated), not a live rewrite
	if !strings.Contains(got, "sole audio track\x1b[0m\n") {
		t.Errorf("sole-track notice must be a permanent line: %q", got)
	}
}

func TestCleanSkipsBroadcast(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "part1.ts")
	writeFile(t, src, 1000)

	ff := &fakeFF{probeFor: func(string) (string, bool) { return libraryJSON(), true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	if tl.untouched != 1 || tl.cleaned != 0 || len(ff.remuxLog) != 0 {
		t.Errorf("broadcast container must be skipped untouched: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "broadcast container") {
		t.Errorf("skip reason missing: %s", out.String())
	}
}

func TestCleanNoVideo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "audio.mkv")
	writeFile(t, src, 1000)

	audioOnly := fmt.Sprintf(`{"streams":[%s],"format":{"duration":"600.0","size":"1000","bit_rate":"1"}}`,
		streamJSON("flac", "audio", "jpn", ""))
	ff := &fakeFF{probeFor: func(string) (string, bool) { return audioOnly, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	if tl.untouched != 1 || !strings.Contains(out.String(), "no real video stream") {
		t.Errorf("audio-only file must be skipped: %+v out=%s", tl, out.String())
	}
}

func TestCleanCountGate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// the temp probe lies about the counts: gate must reject
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(2, 3), true
		}
		return libraryJSON(), true
	}, remuxSize: 1}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	if tl.failed != 1 {
		t.Errorf("count mismatch must fail: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "do not match the plan") {
		t.Errorf("gate message missing: %s", out.String())
	}
}

// Size is deliberately not a gate for a removal-only remux: the payload is
// copied verbatim, so the size only reflects muxer layout economics (audio
// lacing, cluster and index granularity) with nothing to do with
// correctness. Duration and stream-count gates guard the output; even
// gross growth must not block the clean.
func TestCleanSizeNotGated(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(1, 2), true
		}
		return libraryJSON(), true
	}, remuxSize: 2_000_000} // doubles the file: still cleans
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	if tl.cleaned != 1 || tl.failed != 0 {
		t.Errorf("growth must not block the clean: %+v out=%s", tl, out.String())
	}
}

// Regression: a cue-less source gains a full seek index in the remux, so
// removing a tiny stream (cover art, a subtitle) can end up slightly
// bigger than the source. The clean must still succeed.
func TestCleanIndexOverheadPasses(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(1, 2), true
		}
		return libraryJSON(), true
	}, remuxSize: 1_020_000} // index overhead ate the tiny removal savings
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	if tl.cleaned != 1 || tl.failed != 0 {
		t.Errorf("index overhead must not block the clean: %+v out=%s", tl, out.String())
	}
}

func TestCleanMP4Untagged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	writeFile(t, src, 1000)

	mp4JSON := fmt.Sprintf(`{"streams":[%s,%s],"format":{"duration":"60.0","size":"1000","bit_rate":"1","format_name":"mov,mp4"}}`,
		streamJSON("h264", "video", "", ""),
		streamJSON("aac", "audio", "und", ""))
	ff := &fakeFF{probeFor: func(string) (string, bool) { return mp4JSON, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(1, 1, src)

	// untagged MP4 audio counts as kept - nothing to remove
	if tl.untouched != 1 || !strings.Contains(out.String(), "OK (nothing to remove)") {
		t.Errorf("untagged mp4 must be a no-op: %+v out=%s", tl, out.String())
	}
}

func TestRunAllWithIgnores(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mkv")
	b := filepath.Join(dir, "b.mkv")
	writeFile(t, a, 1000)
	writeFile(t, b, 1000)
	if err := os.WriteFile(filepath.Join(dir, ".cleaner-ignore"), []byte("b.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(1, 2), true
		}
		return libraryJSON(), true
	}, remuxSize: 1}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if len(ff.remuxLog) != 1 {
		t.Fatalf("only a.mkv must be cleaned, got %d", len(ff.remuxLog))
	}
	if !hasPair(ff.remuxLog[0], "-i", a) {
		t.Errorf("wrong file: %v", ff.remuxLog[0])
	}
	if !strings.Contains(out.String(), "Skipping (ignored)") {
		t.Errorf("ignore skip line missing: %s", out.String())
	}
}
