package clean

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/media"
	"github.com/hranicka/media-tool/internal/run"
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
		ignores:    ignore.Load(dir, "clean", cfg.Ignores),
		out:        out,
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
	tl.processFile(src)

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

func TestCleanDryRunPlan(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(string) (string, bool) { return libraryJSON(), true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en", DryRun: true}, out)
	tl.processFile(src)

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
	tl.processFile(src)
	if tl.untouched != 1 || len(ff.remuxLog) != 0 {
		t.Errorf("sole tracks must be kept: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "OK (nothing to remove)") {
		t.Errorf("unexpected output: %s", out.String())
	}

	// -e purges them
	ff2 := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(0, 0), true
		}
		return soloJSON, true
	}, remuxSize: 1}
	tl2 := newTestTool(t, dir, ff2, &Config{Target: dir, Langs: "cs,en", PurgeSole: true}, out)
	tl2.processFile(src)
	if tl2.cleaned != 1 {
		t.Errorf("purge must remove sole tracks: %+v", tl2)
	}
	args := ff2.remuxLog[0]
	if !hasPair(args, "-map", "-0:a:0") || !hasPair(args, "-map", "-0:s:0") {
		t.Errorf("purge maps missing: %v", args)
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
	tl.processFile(src)

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
	tl.processFile(src)

	if tl.failed != 1 {
		t.Errorf("count mismatch must fail: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "do not match the plan") {
		t.Errorf("gate message missing: %s", out.String())
	}
}

func TestCleanSizeGate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(1, 2), true
		}
		return libraryJSON(), true
	}, remuxSize: 2_000_000} // grows: rejected
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, Langs: "cs,en"}, out)
	tl.processFile(src)

	if tl.failed != 1 || !strings.Contains(out.String(), "not smaller") {
		t.Errorf("growing output must fail: %+v out=%s", tl, out.String())
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
	tl.processFile(src)

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
