package convert

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/encoders"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
)

// fakeFF fakes both binaries: ffprobe JSON responses keyed by a function of
// the probed path, ffmpeg encodes writing a real output file.
type fakeFF struct {
	probeFor   func(src string) (string, bool)
	streamHook func(args []string) ([]byte, error)
	encodeErr  error
	encStderr  string
	encSize    int64
	encCalls   [][]string
	probeLogs  [][]string
}

func (f *fakeFF) Output(name string, args ...string) ([]byte, error) {
	if name == "ffprobe" {
		src := args[len(args)-1]
		f.probeLogs = append(f.probeLogs, args)
		if f.probeFor != nil {
			if json, ok := f.probeFor(src); ok {
				return []byte(json), nil
			}
		}
		return []byte(`{"streams":[],"format":{}}`), nil
	}
	// encoder probe always succeeds
	return []byte{}, nil
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	f.encCalls = append(f.encCalls, args)
	if f.streamHook != nil {
		return f.streamHook(args)
	}
	return f.defaultStream(args)
}

func (f *fakeFF) defaultStream(args []string) ([]byte, error) {
	if f.encodeErr != nil {
		return []byte(f.encStderr), f.encodeErr
	}
	if err := os.WriteFile(args[len(args)-1], bytes.Repeat([]byte("x"), int(f.encSize)), 0o644); err != nil {
		return nil, err
	}
	return []byte(f.encStderr), nil
}

func videoJSON(codec, pixFmt, extra string) string {
	return fmt.Sprintf(`{"streams":[%s],"format":{"duration":"600.0","size":"1000000","bit_rate":"8000000"}}`,
		fmt.Sprintf(`{"index":0,"codec_name":"%s","codec_type":"video","pix_fmt":"%s","width":1920,"height":1080,"bit_rate":"8000000","disposition":{"attached_pic":0},"color_primaries":"unknown","color_trc":"unknown","color_space":"unknown"%s}`, codec, pixFmt, extra))
}

const h264JSON = `{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","pix_fmt":"yuv420p","width":1920,"height":1080,"bit_rate":"8000000","disposition":{"attached_pic":0},"color_primaries":"unknown","color_trc":"unknown","color_space":"unknown"}],"format":{"duration":"600.0","size":"1000000","bit_rate":"8000000"}}`

// jsonFor responds with h264JSON for the source and the given duration for
// the temp output.
func jsonFor(tmpDur float64) func(string) (string, bool) {
	return func(src string) (string, bool) {
		base := filepath.Base(src)
		if strings.HasSuffix(base, ".tmp") {
			return fmt.Sprintf(`{"streams":[{"index":0,"codec_name":"hevc","codec_type":"video","pix_fmt":"yuv420p","width":1920,"height":1080,"disposition":{"attached_pic":0}}],"format":{"duration":"%.3f","size":"1","bit_rate":"1"}}`, tmpDur), true
		}
		return h264JSON, true
	}
}

func newTestTool(t *testing.T, dir string, ff run.Runner, cfg *Config, out *bytes.Buffer) *tool {
	t.Helper()
	return &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		enc:        encoders.Encoder{Kind: encoders.NVENC, Label: "Nvidia NVENC"},
		cfg:        cfg,
		ignores:    ignore.Load(dir, "convert", cfg.Ignores),
		out:        out,
		guard:      &run.TmpGuard{},
	}
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Repeat([]byte("o"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func firstEncode(t *testing.T, ff *fakeFF) []string {
	t.Helper()
	if len(ff.encCalls) == 0 {
		t.Fatal("expected an ffmpeg encode call")
	}
	return ff.encCalls[0]
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestConvertHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(src)

	if tl.converted != 1 || tl.skipped != 0 || tl.failed != 0 {
		t.Fatalf("counters: %+v (out: %s)", tl, out.String())
	}
	if string(read(t, src))[:10] != "xxxxxxxxxx" {
		t.Error("src must hold the converted content")
	}
	if string(read(t, src+".old"))[:10] != "oooooooooo" {
		t.Error("backup must hold the original content")
	}

	args := firstEncode(t, ff)
	for _, want := range []string{"-map", "0", "-map_metadata", "0", "-map_chapters", "0", "-c:v", "hevc_nvenc", "-cq", "26", "-b:v", "0", "-c:a", "copy", "-c:s", "copy", "-f", "matroska"} {
		if !hasArg(args, want) {
			t.Errorf("encode args missing %q: %v", want, args)
		}
	}
	// -c:v hevc_nvenc must not be followed by the libx265 preset block
	if hasPair(args, "-c:v", "libx265") {
		t.Error("wrong encoder selected")
	}
	if !hasPair(args, "-i", src) {
		t.Errorf("input missing: %v", args)
	}
	if strings.HasSuffix(args[len(args)-1], ".tmp") == false {
		t.Errorf("output must be a temp file, got %s", args[len(args)-1])
	}
}

func TestConvertCoverArtNegativeMap(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1000)

	coverJSON := strings.Replace(h264JSON,
		`}],"format"`,
		`},{"index":1,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}}],"format"`, 1)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return coverJSON, true
	}, encSize: 1}

	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-map", "-0:v:1") {
		t.Errorf("cover art must be negative-mapped: %v", args)
	}
}

func TestConvertMultiVideoSkip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "mvc.mkv")
	writeFile(t, src, 1000)

	mvcJSON := strings.Replace(h264JSON,
		`"streams":[`,
		`"streams":[{"index":1,"codec_name":"h264","codec_type":"video","height":1080,"disposition":{"attached_pic":0}},`, 1)
	ff := &fakeFF{probeFor: func(string) (string, bool) { return mvcJSON, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(src)

	if tl.skipped != 1 || tl.converted != 0 || len(ff.encCalls) != 0 {
		t.Errorf("multi-video must be skipped: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "multiple video streams") {
		t.Errorf("skip reason missing: %s", out.String())
	}
}

func TestConvertDolbyVision(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dv.mkv")
	writeFile(t, src, 1000)

	dvJSON := strings.Replace(h264JSON, `"disposition":{"attached_pic":0}`,
		`"disposition":{"attached_pic":0},"side_data_list":[{"side_data_type":"DOVI configuration record"}]`, 1)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return dvJSON, true
	}, encSize: 1}
	out := &bytes.Buffer{}

	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}
	tl := newTestTool(t, dir, ff, cfg, out)
	tl.processFile(src)
	if tl.skipped != 1 || len(ff.encCalls) != 0 {
		t.Errorf("DV must be skipped without force: %+v", tl)
	}
	if !strings.Contains(out.String(), "Dolby Vision would be lost") {
		t.Errorf("skip reason missing: %s", out.String())
	}

	// forced DV re-encodes
	cfg.Force = true
	tl2 := newTestTool(t, dir, ff, cfg, out)
	tl2.processFile(src)
	if tl2.converted != 1 {
		t.Errorf("forced DV must convert: %+v", tl2)
	}
}

func TestConvertBackupExistsSkip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1000)
	writeFile(t, src+".old", 10)

	ff := &fakeFF{probeFor: jsonFor(600.0)}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(src)

	if tl.skipped != 1 || len(ff.encCalls) != 0 {
		t.Errorf("existing backup must skip: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "backup") {
		t.Errorf("skip reason missing: %s", out.String())
	}
}

func TestConvertCodecGuard(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "already.mkv")
	writeFile(t, src, 1000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return videoJSON("hevc", "yuv420p", ""), true
	}, encSize: 1}
	out := &bytes.Buffer{}

	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}
	tl := newTestTool(t, dir, ff, cfg, out)
	tl.processFile(src)
	if tl.skipped != 1 || len(ff.encCalls) != 0 {
		t.Errorf("non-h264 must be skipped: %+v out=%s", tl, out.String())
	}

	cfg.Force = true
	tl2 := newTestTool(t, dir, ff, cfg, out)
	tl2.processFile(src)
	if tl2.converted != 1 {
		t.Errorf("forced non-h264 must convert: %+v", tl2)
	}
}

func TestConvertSizeGate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	original := bytes.Repeat([]byte("o"), 1_000_000)
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 2_000_000} // grows: rejected
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(src)

	if tl.skipped != 1 {
		t.Errorf("grown output must be skipped: %+v out=%s", tl, out.String())
	}
	if !bytes.Equal(read(t, src), original) {
		t.Error("original must stay untouched when the gate rejects")
	}
	leftovers, _ := filepath.Glob(src + ".*tmp")
	if len(leftovers) != 0 {
		t.Errorf("temp file must be cleaned up: %v", leftovers)
	}
}

func TestConvertDurationGate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	original := bytes.Repeat([]byte("o"), 1000)
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: jsonFor(100.0), encSize: 500} // output much shorter
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(src)

	if tl.failed != 1 {
		t.Errorf("truncated output must fail: %+v out=%s", tl, out.String())
	}
	if !bytes.Equal(read(t, src), original) {
		t.Error("original must stay untouched when the duration gate rejects")
	}
}

func TestConvertDryRun(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0)}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, DryRun: true}, out)
	tl.processFile(src)

	if tl.converted != 1 || len(ff.encCalls) != 0 {
		t.Errorf("dry run must not encode: %+v out=%s", tl, out.String())
	}
	if !strings.Contains(out.String(), "Would convert") || !strings.Contains(out.String(), "CQ 26 quality target") {
		t.Errorf("plan line missing: %s", out.String())
	}
}

func TestConvertChromecast(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mp4")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeChromecast, CQ: 22}, out)
	tl.processFile(src)

	outPath := filepath.Join(dir, "movie_CC4_FIXED.mkv")
	if tl.converted != 1 {
		t.Fatalf("chromecast must create output: %+v out=%s", tl, out.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("chromecast mode must keep the original in place")
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-f", "matroska") || hasPair(args, "-movflags", "+use_metadata_tags") {
		t.Errorf("chromecast output must be plain matroska: %v", args)
	}
}

func TestConvertChromecastIdempotence(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie_CC4_FIXED.mkv")
	writeFile(t, src, 1000)

	ff := &fakeFF{probeFor: jsonFor(600.0)}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeChromecast, CQ: 22}, out)
	tl.processFile(src)

	if tl.skipped != 1 || len(ff.encCalls) != 0 {
		t.Errorf("CC4_FIXED outputs must be skipped: %+v out=%s", tl, out.String())
	}
}

func TestConvertMP4MuxArgs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mp4")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(src)

	args := firstEncode(t, ff)
	if !hasPair(args, "-movflags", "+use_metadata_tags") || !hasPair(args, "-f", "mp4") {
		t.Errorf("mp4 mux args missing: %v", args)
	}
}

func TestConvertCameraCap(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	out := &bytes.Buffer{}
	// stream bit_rate 8000000 -> 8000 kbps -> cap 5200k
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeCamera, CQ: 28}, out)
	tl.processFile(src)

	args := firstEncode(t, ff)
	if !hasPair(args, "-maxrate", "5200k") || !hasPair(args, "-bufsize", "10400k") {
		t.Errorf("camera cap args missing: %v", args)
	}
	if !strings.Contains(out.String(), "capped at 5200k (source 8000k)") {
		t.Errorf("plan line missing: %s", out.String())
	}
}

func TestConvertColorArgs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hdr.mkv")
	writeFile(t, src, 1_000_000)

	hdrJSON := strings.Replace(h264JSON,
		`"color_primaries":"unknown","color_trc":"unknown","color_space":"unknown"`,
		`"color_primaries":"bt2020","color_trc":"smpte2084","color_space":"bt2020nc","pix_fmt":"yuv420p10le"`, 1)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return hdrJSON, true
	}, encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(src)

	args := firstEncode(t, ff)
	if !hasPair(args, "-pix_fmt", "p010le") || !hasPair(args, "-color_primaries", "bt2020") ||
		!hasPair(args, "-color_trc", "smpte2084") || !hasPair(args, "-colorspace", "bt2020nc") {
		t.Errorf("HDR passthrough args missing: %v", args)
	}
}

func TestConvertScale(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// a 2160p source gets downscaled to the 1080p target
	uhdJSON := strings.Replace(h264JSON, `"height":1080`, `"height":2160`, 1)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return uhdJSON, true
	}, encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, ScaleHeight: 1080}, &bytes.Buffer{})
	tl.processFile(src)

	args := firstEncode(t, ff)
	if !hasPair(args, "-vf", "scale=-2:1080:flags=lanczos") {
		t.Errorf("scale filter missing: %v", args)
	}
}

func TestConvertScaleSkipsSmaller(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, ScaleHeight: 2160}, out)
	tl.processFile(src)

	args := firstEncode(t, ff)
	if hasArg(args, "-vf") {
		t.Errorf("no downscale expected: %v", args)
	}
	if !strings.Contains(out.String(), "no downscale to 2160p") {
		t.Errorf("scale info line missing: %s", out.String())
	}
}

func TestConvertRetryLadder(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	calls := 0
	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	ff.streamHook = func(args []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("error while decoding slice\n"), fmt.Errorf("boom")
		}
		return ff.defaultStream(args)
	}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(src)

	if tl.converted != 1 || calls != 2 {
		t.Errorf("expected a retry after the failed attempt: calls=%d %+v", calls, tl)
	}
	args := ff.encCalls[1]
	if !hasArg(args, "-sn") || !hasArg(args, "-dn") {
		t.Errorf("second attempt must drop subtitles: %v", args)
	}
}

func TestConvertDecodeErrorWarning(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "damaged.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000,
		encStderr: "some noise\n[hevc] error while decoding MB 42\nmore noise\n"}
	// the warning goes to stderr (bash parity) - capture it
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(src)
	os.Stderr = oldErr
	w.Close()
	warn, _ := io.ReadAll(r)

	if tl.converted != 1 {
		t.Fatalf("convert expected: %+v", tl)
	}
	if !strings.Contains(string(warn), "damaged video data (1 decode errors)") {
		t.Errorf("decode-error warning missing: %s", warn)
	}
}

func TestRunAllWithIgnores(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mkv")
	b := filepath.Join(dir, "b.mkv")
	writeFile(t, a, 1000)
	writeFile(t, b, 1000)
	if err := os.WriteFile(filepath.Join(dir, ".hevcconverter-ignore"), []byte("b.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 1}
	out := &bytes.Buffer{}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}
	tl := newTestTool(t, dir, ff, cfg, out)
	tl.ignores = ignore.Load(dir, "convert", nil)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if len(ff.encCalls) != 1 {
		t.Errorf("only a.mkv must encode, got %d encodes", len(ff.encCalls))
	}
	if !hasPair(ff.encCalls[0], "-i", a) {
		t.Errorf("wrong file encoded: %v", ff.encCalls[0])
	}
	if !strings.Contains(out.String(), "Skipping (ignored)") || !strings.Contains(out.String(), "b.mkv") {
		t.Errorf("ignore skip line missing: %s", out.String())
	}
}

func TestParseScaleHeight(t *testing.T) {
	cases := map[string]int{"1080p": 1080, "720": 720, "1081p": 1080, "": 0}
	for in, want := range cases {
		got, err := parseScaleHeight(in)
		if err != nil || got != want {
			t.Errorf("parseScaleHeight(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseScaleHeight("abc"); err == nil {
		t.Error("invalid height must fail")
	}
}

func TestConvertVAAPIEncode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// 10-bit HDR source: vaapi uploads p010 frames, never -pix_fmt
	hdrJSON := strings.Replace(h264JSON,
		`"color_primaries":"unknown","color_trc":"unknown","color_space":"unknown"`,
		`"color_primaries":"bt2020","color_trc":"smpte2084","color_space":"bt2020nc","pix_fmt":"yuv420p10le"`, 1)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return hdrJSON, true
	}, encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, VaapiDevice: "/dev/dri/renderD129"}, &bytes.Buffer{})
	tl.enc = encoders.Encoder{Kind: encoders.VAAPI, Label: "VAAPI (Intel/AMD)"}
	tl.processFile(src)

	args := firstEncode(t, ff)
	if !hasPair(args, "-vaapi_device", "/dev/dri/renderD129") {
		t.Errorf("real encode must init the vaapi device: %v", args)
	}
	if !hasPair(args, "-vf", "format=p010le,hwupload") {
		t.Errorf("10-bit vaapi filter chain missing: %v", args)
	}
	if hasPair(args, "-pix_fmt", "p010le") {
		t.Errorf("vaapi must not get -pix_fmt: %v", args)
	}
	if !hasPair(args, "-c:v", "hevc_vaapi") {
		t.Errorf("wrong encoder: %v", args)
	}
}

func TestSelectEncoder(t *testing.T) {
	out := &bytes.Buffer{}
	failNVENC := &failFirstRunner{failProbe: "hevc_nvenc"}
	enc, err := selectEncoder(failNVENC, out, "ffmpeg", "/dev/dri/renderD128", encoders.Candidates())
	if err != nil {
		t.Fatalf("selectEncoder: %v", err)
	}
	if enc.Kind != encoders.QSV {
		t.Errorf("expected qsv after nvenc failure, got %s", enc.Kind)
	}
	if !strings.Contains(out.String(), "Testing encoder: Nvidia NVENC...") {
		t.Errorf("probe progress line missing label: %s", out.String())
	}
	if !strings.Contains(out.String(), "Testing encoder: Intel QuickSync...") {
		t.Errorf("probe progress line missing label: %s", out.String())
	}
}

// failFirstRunner fails every ffmpeg run whose args target the given encoder.
type failFirstRunner struct {
	failProbe string
}

func (f *failFirstRunner) Output(name string, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == f.failProbe {
			return nil, fmt.Errorf("probe fail")
		}
	}
	return []byte{}, nil
}

func (f *failFirstRunner) Stream(name string, args ...string) ([]byte, error) {
	return f.Output(name, args...)
}

func TestSourceVideoKbpsTiers(t *testing.T) {
	// tier 1: video stream bitrate
	v := probe.Stream{BitRate: "8000000"}
	if got := sourceVideoKbps(v, &probe.Info{}, 0); got != 8000 {
		t.Errorf("stream tier: got %d, want 8000", got)
	}
	// tier 2: container bitrate
	v.BitRate = ""
	info := &probe.Info{Format: probe.Format{BitRate: "4000000"}}
	if got := sourceVideoKbps(v, info, 0); got != 4000 {
		t.Errorf("container tier: got %d, want 4000", got)
	}
	// tier 3: size / integer-truncated duration
	info.Format.BitRate = ""
	info.Format.Duration = "600.7"
	if got := sourceVideoKbps(v, info, 600_000_000); got != 8000 {
		t.Errorf("size/duration tier: got %d, want 8000", got)
	}
	// nothing available
	if got := sourceVideoKbps(v, &probe.Info{}, 0); got != 0 {
		t.Errorf("no-data case: got %d, want 0", got)
	}
}
