package convert

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hranicka/mediatool/internal/encoders"
	"github.com/hranicka/mediatool/internal/media"
)

// --- preset & flag resolution -------------------------------------------------

func TestLookupPreset(t *testing.T) {
	p, err := Lookup("tv")
	if err != nil {
		t.Fatalf("Lookup(tv): %v", err)
	}
	if p.Mode != encoders.ModeMovie || p.ScaleHeight != 1080 || !p.Tonemap || p.Audio != "ac3" {
		t.Errorf("tv preset mismatch: %+v", p)
	}
	p, err = Lookup("camera")
	if err != nil {
		t.Fatalf("Lookup(camera): %v", err)
	}
	if p.Mode != encoders.ModeCamera || p.Audio != "aac:128k" || p.Tonemap || p.ScaleHeight != 0 {
		t.Errorf("camera preset mismatch: %+v", p)
	}
	p, err = Lookup("movie")
	if err != nil {
		t.Fatalf("Lookup(movie): %v", err)
	}
	if p.Mode != encoders.ModeMovie || p.CQ != 22 || p.Tonemap || p.ScaleHeight != 0 || p.Audio != "" {
		t.Errorf("movie preset mismatch: %+v", p)
	}
	if _, err := Lookup("bogus"); err == nil || !strings.Contains(err.Error(), "movie") {
		t.Errorf("unknown preset must list the names: %v", err)
	}
}

func TestApplyPreset(t *testing.T) {
	p, err := Lookup("tablet")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	scale, audio := "", ""
	applyPreset(p, map[string]bool{}, &cfg, &scale, &audio)
	if cfg.Mode != encoders.ModeMovie || cfg.CQ != 33 || scale != "720p" || audio != "aac:192k:2" || !cfg.Tonemap {
		t.Errorf("tablet defaults not applied: cfg=%+v scale=%q audio=%q", cfg, scale, audio)
	}

	// explicitly passed flags win over the preset
	cfg2 := Config{}
	scale2, audio2 := "1080p", "copy"
	applyPreset(p, map[string]bool{"q": true, "r": true, "a": true, "tonemap": true}, &cfg2, &scale2, &audio2)
	if cfg2.CQ != 0 || cfg2.Tonemap || scale2 != "1080p" || audio2 != "copy" {
		t.Errorf("explicit flags must win: cfg=%+v scale=%q audio=%q", cfg2, scale2, audio2)
	}
}

func TestParseAudio(t *testing.T) {
	cases := []struct {
		in   string
		want audioTarget
	}{
		{"copy", audioTarget{kind: "copy"}},
		{"aac", audioTarget{kind: "aac", bitrate: "192k"}},
		{"aac:128k", audioTarget{kind: "aac", bitrate: "128k"}},
		{"aac:128", audioTarget{kind: "aac", bitrate: "128k"}},
		{"aac:128k:2", audioTarget{kind: "aac", bitrate: "128k", channels: 2}},
		{"ac3", audioTarget{kind: "ac3", bitrate: "640k"}},
		{"ac3:448", audioTarget{kind: "ac3", bitrate: "448k"}},
	}
	for _, c := range cases {
		got, err := parseAudio(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseAudio(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"foo", "aac:x", "aac:192k:0", "copy:1", "aac:1:2:3", "ac3:x"} {
		if _, err := parseAudio(bad); err == nil {
			t.Errorf("parseAudio(%q) must fail", bad)
		}
	}
}

// --- audio targets ------------------------------------------------------------

const dtsAudio = `{"index":1,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"cs"}}`
const aacAudio = `{"index":2,"codec_name":"aac","codec_type":"audio","channels":2,"bit_rate":"192000","tags":{"language":"cs"}}`
const flacAudio = `{"index":2,"codec_name":"flac","codec_type":"audio","tags":{"language":"en"}}`

func withAudio(base, audio string) string {
	return strings.Replace(base, `],"format"`, `,`+audio+`],"format"`, 1)
}

// tmpJSON builds a temp-output probe response: the hevc video stream plus
// any extra streams (the stream-count gate compares against these).
func tmpJSON(dur float64, streams ...string) string {
	video := `{"index":0,"codec_name":"hevc","codec_type":"video","pix_fmt":"yuv420p","width":1920,"height":1080,"disposition":{"attached_pic":0}}`
	all := append([]string{video}, streams...)
	return fmt.Sprintf(`{"streams":[%s],"format":{"duration":"%.3f","size":"1","bit_rate":"1"}}`, strings.Join(all, ","), dur)
}

func tmpWithAudio(dur float64, audio []string) string {
	return tmpJSON(dur, audio...)
}

func TestConvertAudioAAC(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// dts (ordinal 0) is transcoded, the existing aac track (ordinal 1) copied
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return withAudio(h264JSON, dtsAudio+","+aacAudio), true
	}, encSize: 500_000}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Audio: "aac:192k:2"}
	tl := newTestTool(t, dir, ff, cfg, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %s", runOutput(tl))
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-c:a:0", "aac") || !hasPair(args, "-b:a:0", "192k") || !hasPair(args, "-ac:0", "2") {
		t.Errorf("dts track must transcode to aac 192k stereo: %v", args)
	}
	if !hasPair(args, "-c:a:1", "copy") {
		t.Errorf("existing aac track must be copied: %v", args)
	}
	if hasPair(args, "-c:a", "copy") {
		t.Errorf("aac target must not carry the blanket copy: %v", args)
	}
}

func TestConvertAudioAC3Append(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// dts (cs, ordinal 0) and flac (en, ordinal 1) both need AC3; the
	// appended copies land after all existing audio (ordinals 2 and 3)
	// and must survive into the output (audio-count gate)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			ac3Stream := `{"index":9,"codec_name":"ac3","codec_type":"audio","channels":6,"bit_rate":"640000"}`
			dtsStream := `{"index":10,"codec_name":"dts","codec_type":"audio","channels":6}`
			return tmpWithAudio(600.0, []string{ac3Stream, dtsStream, ac3Stream, dtsStream}), true
		}
		return withAudio(h264JSON, dtsAudio+","+flacAudio), true
	}, encSize: 500_000}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Audio: "ac3"}
	tl := newTestTool(t, dir, ff, cfg, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 || tl.failed != 0 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-map", "0:a:0") || !hasPair(args, "-c:a:2", "ac3") || !hasPair(args, "-b:a:2", "640k") {
		t.Errorf("cs dts append missing: %v", args)
	}
	if !hasPair(args, "-map", "0:a:1") || !hasPair(args, "-c:a:3", "ac3") || !hasPair(args, "-b:a:3", "640k") {
		t.Errorf("en flac append missing: %v", args)
	}
}

func TestConvertAudioGateRejects(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	original := bytes.Repeat([]byte("o"), 1_000_000)
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// the output lost the appended AC3 track: keep the original
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			dtsStream := `{"index":10,"codec_name":"dts","codec_type":"audio","channels":6}`
			return tmpWithAudio(600.0, []string{dtsStream}), true
		}
		return withAudio(h264JSON, dtsAudio), true
	}, encSize: 500_000}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Audio: "ac3"}
	tl := newTestTool(t, dir, ff, cfg, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.failed != 1 {
		t.Fatalf("missing appended track must fail the gate: %+v", tl)
	}
	if !bytes.Equal(read(t, src), original) {
		t.Error("original must stay untouched when the audio gate rejects")
	}
}

// --- tonemap ------------------------------------------------------------------

// uhdHDRJSON is a 2160p 10-bit HDR10 source.
var uhdHDRJSON = strings.Replace(h264JSON,
	`"pix_fmt":"yuv420p","width":1920,"height":1080`,
	`"pix_fmt":"yuv420p10le","width":3840,"height":2160`, 1)

var hdrColors = strings.Replace(uhdHDRJSON,
	`"color_primaries":"unknown","color_transfer":"unknown","color_space":"unknown"`,
	`"color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc"`, 1)

const cudaTonemapVF = "scale_cuda=1280:720,tonemap_cuda=tonemap=bt2390:desat=0:primaries=bt709:transfer=bt709:matrix=bt709:format=nv12"

func TestConvertTonemapNVENC(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return hdrColors, true
	}, encSize: 500_000}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, ScaleHeight: 720, Tonemap: true}
	tl := newTestTool(t, dir, ff, cfg, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-hwaccel", "cuda") || !hasPair(args, "-hwaccel_output_format", "cuda") {
		t.Errorf("tonemap must decode with CUDA-resident frames: %v", args)
	}
	if !hasPair(args, "-vf", cudaTonemapVF) {
		t.Errorf("cuda tonemap chain missing: %v", args)
	}
	if hasPair(args, "-pix_fmt", "p010le") {
		t.Errorf("tonemapped SDR output must not carry the HDR pixel format: %v", args)
	}
	if !hasPair(args, "-color_trc", "bt709") || !hasPair(args, "-color_primaries", "bt709") {
		t.Errorf("tonemapped output must signal SDR bt709: %v", args)
	}
	if hasArg(args, "smpte2084") {
		t.Errorf("HDR transfer leaked onto the tonemapped output: %v", args)
	}
}

func TestConvertTonemapVAAPIFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// non-NVENC backend: tonemap in software through libplacebo
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return hdrColors, true
	}, encSize: 500_000}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, ScaleHeight: 720, Tonemap: true}
	tl := newTestTool(t, dir, ff, cfg, &bytes.Buffer{})
	tl.enc = encoders.Encoder{Codec: encoders.CodecHEVC, Kind: encoders.VAAPI, Label: "VAAPI (Intel/AMD)"}
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	wantVF := "scale=-2:720:flags=lanczos," + placeboTonemap + ",format=nv12,hwupload"
	if !hasPair(args, "-vf", wantVF) {
		t.Errorf("libplacebo fallback chain missing: %v", args)
	}
	if hasPair(args, "-pix_fmt", "p010le") {
		t.Errorf("tonemapped output must be 8-bit: %v", args)
	}
	if hasArg(args, "p010le") {
		t.Errorf("10-bit fragment leaked into the tonemapped chain: %v", args)
	}
}

// failFilterRunner fails the libplacebo probe while delegating everything
// else to the fake ffmpeg/ffprobe pair.
type failFilterRunner struct {
	ff         *fakeFF
	failFilter string
}

func (f *failFilterRunner) Output(name string, args ...string) ([]byte, error) {
	for i, a := range args {
		if a == "-vf" && i+1 < len(args) && args[i+1] == f.failFilter {
			return nil, fmt.Errorf("filter not available")
		}
	}
	return f.ff.Output(name, args...)
}

func (f *failFilterRunner) Stream(name string, args ...string) ([]byte, error) {
	return f.ff.Stream(name, args...)
}

func TestConvertTonemapUnavailableSkips(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	original := bytes.Repeat([]byte("o"), 1_000_000)
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// no libplacebo in the build and no CUDA path (VAAPI): the file is
	// skipped untouched rather than tonemapped wrongly
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return hdrColors, true
	}}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Tonemap: true}
	tl := newTestTool(t, dir, &failFilterRunner{ff: ff, failFilter: placeboTonemap}, cfg, &bytes.Buffer{})
	tl.enc = encoders.Encoder{Codec: encoders.CodecHEVC, Kind: encoders.VAAPI, Label: "VAAPI (Intel/AMD)"}
	tl.processFile(1, 1, src)

	if tl.skipped != 1 || tl.converted != 0 || len(ff.encCalls) != 0 {
		t.Fatalf("unavailable tonemap must skip: %+v", tl)
	}
	if !bytes.Equal(read(t, src), original) {
		t.Error("original must stay untouched")
	}
}

func TestConvertTonemapSDRUnaffected(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// an SDR source in a tonemap run keeps the normal ladder
	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	cfg := &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Tonemap: true}
	tl := newTestTool(t, dir, ff, cfg, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	if hasArg(args, "tonemap_cuda") || hasArg(args, "libplacebo") {
		t.Errorf("SDR source must not tonemap: %v", args)
	}
	if !hasPair(args, "-hwaccel", "auto") {
		t.Errorf("SDR source must use the normal decode ladder: %v", args)
	}
}

// --- deinterlace, containers, mtime, single file ------------------------------

func TestConvertDeinterlace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cam.mts")
	writeFile(t, src, 1_000_000)

	interlacedJSON := strings.Replace(h264JSON, `"disposition"`, `"field_order":"tb","disposition"`, 1)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return jsonFor(600.0)(src)
		}
		return interlacedJSON, true
	}, encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-vf", "yadif=mode=send_frame:parity=auto") {
		t.Errorf("interlaced source must deinterlace: %v", args)
	}
	if !strings.Contains(out.String(), "interlaced source (tb): deinterlacing") {
		t.Errorf("deinterlace note missing: %s", out.String())
	}
}

func TestConvertMTSOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cam.mts")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-fflags", "+genpts") {
		t.Errorf("camera container must genpts: %v", args)
	}
	if !hasPair(args, "-f", "mp4") || !hasPair(args, "-movflags", "+use_metadata_tags+faststart") {
		t.Errorf("non-mkv source must mux to mp4: %v", args)
	}
}

func TestConvertTSWithSubsToMKV(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "part1.ts")
	writeFile(t, src, 1_000_000)

	// video+audio+subtitle: MP4 would lose the subtitle, so the output is
	// MKV and the file is renamed part1.ts -> part1.mkv (original .old)
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return tmpJSON(600.0, aacAudio, subCS), true
		}
		return withAudio(withAudio(h264JSON, aacAudio), subCS), true
	}, encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.processFile(1, 1, src)

	if tl.converted != 1 || tl.failed != 0 {
		t.Fatalf("counters: %+v (out: %s)", tl, out.String())
	}
	dst := filepath.Join(dir, "part1.mkv")
	if string(read(t, dst))[:10] != "xxxxxxxxxx" {
		t.Error("part1.mkv must hold the converted content")
	}
	if string(read(t, src+".old"))[:10] != "oooooooooo" {
		t.Error("part1.ts.old must hold the original content")
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("the original part1.ts must be renamed away")
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-f", "matroska") || hasArg(args, "-movflags") {
		t.Errorf("subtitle source must mux to matroska: %v", args)
	}
	if !strings.Contains(out.String(), "output: "+dst) {
		t.Errorf("rename must be reported: %s", out.String())
	}
}

func TestConvertTSVideoOnlyToMP4(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cam.ts")
	writeFile(t, src, 1_000_000)

	// video-only: nothing to lose, so MP4 (renamed cam.ts -> cam.mp4)
	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	dst := filepath.Join(dir, "cam.mp4")
	if string(read(t, dst))[:10] != "xxxxxxxxxx" {
		t.Error("cam.mp4 must hold the converted content")
	}
	if string(read(t, src+".old"))[:10] != "oooooooooo" {
		t.Error("cam.ts.old must hold the original content")
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("the original cam.ts must be renamed away")
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-f", "mp4") || !hasPair(args, "-movflags", "+use_metadata_tags+faststart") {
		t.Errorf("video-only source must mux to mp4: %v", args)
	}
}

func TestConvertMP4WithSubsToMKV(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	writeFile(t, src, 1_000_000)

	// the rule is uniform: an mp4 carrying subtitles also goes to MKV
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return tmpJSON(600.0, aacAudio, subCS), true
		}
		return withAudio(withAudio(h264JSON, aacAudio), subCS), true
	}, encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	dst := filepath.Join(dir, "clip.mkv")
	if string(read(t, dst))[:10] != "xxxxxxxxxx" {
		t.Error("clip.mkv must hold the converted content")
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("the original clip.mp4 must be renamed away")
	}
	if !hasPair(firstEncode(t, ff), "-f", "matroska") {
		t.Errorf("mp4 with subtitles must mux to matroska: %v", firstEncode(t, ff))
	}
}

func TestConvertMtimePreserved(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)
	t0 := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(src, t0, t0); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v", tl)
	}
	st, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(t0) {
		t.Errorf("mtime must be restored from the source: got %v, want %v", st.ModTime(), t0)
	}
}

func TestConvertSingleFileTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: jsonFor(600.0), encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: src, Mode: encoders.ModeMovie, CQ: 26}, &bytes.Buffer{})
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}
	if tl.converted != 1 || tl.skipped != 0 {
		t.Errorf("single-file target must process the file: %+v", tl)
	}
	if !hasPair(firstEncode(t, ff), "-i", src) {
		t.Errorf("wrong input: %v", firstEncode(t, ff))
	}
}

// runOutput returns the recorded tool counters as a compact string for
// failure messages (the tool struct itself does not print its state).
func runOutput(tl *tool) string {
	return fmt.Sprintf("converted=%d skipped=%d failed=%d", tl.converted, tl.skipped, tl.failed)
}

// --- language cleanup (-l) ------------------------------------------------------

const subCS = `{"index":3,"codec_name":"srt","codec_type":"subtitle","tags":{"language":"cs"}}`
const subDE = `{"index":4,"codec_name":"srt","codec_type":"subtitle","tags":{"language":"de"}}`

func TestConvertLangCleanup(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// german audio + german subtitle outside the cs,en whitelist: both
	// dropped, the kept cs tracks and their counts must survive
	audio := `{"index":1,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"de"}},{"index":2,"codec_name":"aac","codec_type":"audio","channels":2,"bit_rate":"192000","tags":{"language":"cs"}}`
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			aacStream := `{"index":9,"codec_name":"aac","codec_type":"audio","channels":2,"bit_rate":"192000","tags":{"language":"cs"}}`
			return tmpJSON(600.0, aacStream, subCS), true
		}
		return withAudio(withAudio(h264JSON, audio), subCS+","+subDE), true
	}, encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Langs: "cs,en"}, out)
	tl.whitelist = media.ExpandWhitelist([]string{"cs", "en"})
	tl.processFile(1, 1, src)

	if tl.converted != 1 || tl.failed != 0 {
		t.Fatalf("converted: %s (out: %s)", runOutput(tl), out.String())
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-map", "-0:a:0") || !hasPair(args, "-map", "-0:s:1") {
		t.Errorf("non-whitelisted tracks must be negative-mapped: %v", args)
	}
	for _, want := range []string{"remove audio #0 (dts, lang 'de')", "remove subtitle #1 (srt, lang 'de')"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("cleanup line %q missing: %s", want, out.String())
		}
	}
}

func TestConvertLangReservedQaaKept(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// cs audio plus a reserved local-use "qaa" track (broadcast original
	// audio): qaa carries no real language, so it stays even though it is
	// neither whitelisted nor the sole track
	csAudio := `{"index":1,"codec_name":"ac3","codec_type":"audio","channels":6,"bit_rate":"448000","tags":{"language":"cs"}}`
	qaaAudio := `{"index":2,"codec_name":"mp2","codec_type":"audio","channels":2,"bit_rate":"192000","tags":{"language":"qaa"}}`
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return tmpJSON(600.0, csAudio, qaaAudio), true
		}
		return withAudio(h264JSON, csAudio+","+qaaAudio), true
	}, encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Langs: "cs,en"}, out)
	tl.whitelist = media.ExpandWhitelist([]string{"cs", "en"})
	tl.processFile(1, 1, src)

	if tl.converted != 1 || tl.failed != 0 {
		t.Fatalf("converted: %s (out: %s)", runOutput(tl), out.String())
	}
	args := firstEncode(t, ff)
	if hasPair(args, "-map", "-0:a:0") || hasPair(args, "-map", "-0:a:1") {
		t.Errorf("reserved qaa track must not be dropped: %v", args)
	}
}

func TestConvertLangSoleAudioKept(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// the only audio track stays even when its language is not whitelisted
	// sole german audio track: outside cs,en but the only one, so it stays
	germanDTS := `{"index":1,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"de"}}`
	keptDTS := `{"index":9,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"de"}}`
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return tmpJSON(600.0, keptDTS), true
		}
		return withAudio(h264JSON, germanDTS), true
	}, encSize: 500_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26}, out)
	tl.whitelist = media.ExpandWhitelist([]string{"cs", "en"})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %s (out: %s)", runOutput(tl), out.String())
	}
	if hasArg(firstEncode(t, ff), "-0:a:") {
		t.Errorf("sole audio track must be kept: %v", firstEncode(t, ff))
	}
	if !strings.Contains(out.String(), "sole audio track") {
		t.Errorf("sole-track note missing: %s", out.String())
	}
}

func TestConvertLangAACOrdinalShift(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// german dts (input ordinal 0) dropped, czech dts (input ordinal 1)
	// kept: the kept track's output ordinal is 0, not 1
	germanDTS := `{"index":1,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"de"}}`
	czechDTS := `{"index":2,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"cs"}}`
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			keptDTS := `{"index":9,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"cs"}}`
			return tmpJSON(600.0, keptDTS), true
		}
		return withAudio(h264JSON, germanDTS+","+czechDTS), true
	}, encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Audio: "aac:192k:2"}, &bytes.Buffer{})
	tl.whitelist = media.ExpandWhitelist([]string{"cs", "en"})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %s", runOutput(tl))
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-map", "-0:a:0") {
		t.Errorf("german track must be dropped: %v", args)
	}
	if !hasPair(args, "-c:a:0", "aac") || !hasPair(args, "-ac:0", "2") {
		t.Errorf("kept track must encode at output ordinal 0: %v", args)
	}
	if hasPair(args, "-c:a:1", "aac") {
		t.Errorf("dropped track must not leave stale per-stream args: %v", args)
	}
}

func TestConvertLangAC3AppendFiltered(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	// czech dts (kept, input ordinal 0) gets the appended AC3 at output
	// ordinal 1; the dropped german truehd gets none
	germanTrueHD := `{"index":2,"codec_name":"truehd","codec_type":"audio","channels":8,"tags":{"language":"de"}}`
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			dtsStream := `{"index":9,"codec_name":"dts","codec_type":"audio","channels":6}`
			ac3Stream := `{"index":10,"codec_name":"ac3","codec_type":"audio","channels":6,"bit_rate":"640000"}`
			return tmpWithAudio(600.0, []string{dtsStream, ac3Stream}), true
		}
		return withAudio(h264JSON, dtsAudio+","+germanTrueHD), true
	}, encSize: 500_000}
	tl := newTestTool(t, dir, ff, &Config{Dir: dir, Mode: encoders.ModeMovie, CQ: 26, Audio: "ac3"}, &bytes.Buffer{})
	tl.whitelist = media.ExpandWhitelist([]string{"cs", "en"})
	tl.processFile(1, 1, src)

	if tl.converted != 1 || tl.failed != 0 {
		t.Fatalf("converted: %s", runOutput(tl))
	}
	args := firstEncode(t, ff)
	if !hasPair(args, "-map", "0:a:0") || !hasPair(args, "-c:a:1", "ac3") || !hasPair(args, "-b:a:1", "640k") {
		t.Errorf("kept dts must get the appended AC3 at output ordinal 1: %v", args)
	}
	if hasArg(args, "0:a:1") {
		t.Errorf("dropped truehd must not be re-mapped: %v", args)
	}
}
