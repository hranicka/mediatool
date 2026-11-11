package ac3

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
)

type fakeFF struct {
	probeFor  func(src string) (string, bool)
	encSize   int64
	encodeLog [][]string
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
	f.encodeLog = append(f.encodeLog, args)
	if err := os.WriteFile(args[len(args)-1], make([]byte, f.encSize), 0o644); err != nil {
		return nil, err
	}
	return nil, nil
}

func audioJSON(codec, lang, title, br string, ch int) string {
	parts := []string{
		`"index":0`,
		fmt.Sprintf(`"codec_name":"%s"`, codec),
		`"codec_type":"audio"`,
		fmt.Sprintf(`"channels":%d`, ch),
	}
	if br != "" {
		parts = append(parts, fmt.Sprintf(`"bit_rate":"%s"`, br))
	}
	var tags []string
	if lang != "" {
		tags = append(tags, fmt.Sprintf(`"language":"%s"`, lang))
	}
	if title != "" {
		tags = append(tags, fmt.Sprintf(`"title":"%s"`, title))
	}
	if len(tags) > 0 {
		parts = append(parts, fmt.Sprintf(`"tags":{%s}`, strings.Join(tags, ",")))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func videoJSON() string {
	return `{"index":0,"codec_name":"h264","codec_type":"video","disposition":{"attached_pic":0}}`
}

// mixedJSON: dts eng, commentary ac3 eng, truehd ces - the commentary AC3
// must not count as a valid conversion of eng.
func mixedJSON() string {
	return fmt.Sprintf(`{"streams":[%s,%s,%s,%s],"format":{"duration":"600.0","size":"1000000","bit_rate":"8000000"}}`,
		videoJSON(),
		audioJSON("dts", "eng", "", "1536000", 6),
		audioJSON("ac3", "eng", "Director's commentary", "192000", 2),
		audioJSON("truehd", "ces", "", "", 0))
}

func remuxJSON(total, ac3Count int) string {
	var streams []string
	streams = append(streams, videoJSON())
	for i := 0; i < total; i++ {
		if i < ac3Count {
			streams = append(streams, audioJSON("ac3", "", "", "", 6))
		} else {
			streams = append(streams, audioJSON("dts", "", "", "", 6))
		}
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
		ignores:    mustIgnore(dir, "ac3", cfg.Ignores),
		rep:        ui.New(out, ui.Profile{}),
		guard:      &run.TmpGuard{},
	}
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

func TestAC3HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(5, 3), true // 2 original audio + 2 appended, 1 commentary + 2 appended ac3
		}
		return mixedJSON(), true
	}, encSize: 1_200_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000}, out)
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("converted: %+v out=%s", tl, out.String())
	}
	args := ff.encodeLog[0]
	// the original tracks are copied; the AC3 copies are appended after all
	// existing audio streams (srcTotal=3 -> out ordinals 3 and 4)
	if !hasPair(args, "-map", "0:a:0") || !hasPair(args, "-c:a:3", "ac3") || !hasPair(args, "-b:a:3", "640k") {
		t.Errorf("dts track not appended as ac3: %v", args)
	}
	if !hasPair(args, "-map", "0:a:2") || !hasPair(args, "-c:a:4", "ac3") || !hasPair(args, "-b:a:4", "640k") {
		t.Errorf("truehd track not appended as ac3: %v", args)
	}
	if !hasPair(args, "-c:v", "copy") || !hasPair(args, "-c:s", "copy") || !hasPair(args, "-f", "matroska") {
		t.Errorf("copy passthrough args wrong: %v", args)
	}
	if _, err := os.Stat(src + ".old"); err != nil {
		t.Error("backup missing")
	}
}

func TestAC3NoBackup(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(5, 3), true
		}
		return mixedJSON(), true
	}, encSize: 1_200_000}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000, NoBackup: true}, out)
	tl.processFile(1, 1, src)

	if tl.converted != 1 || tl.failed != 0 {
		t.Fatalf("counters: converted=%d failed=%d out=%s", tl.converted, tl.failed, out.String())
	}
	if _, err := os.Stat(src + ".old"); err == nil {
		t.Error("-no-backup must not create a backup")
	}
	st, err := os.Stat(src)
	if err != nil {
		t.Fatalf("replacement missing: %v", err)
	}
	if st.Size() != 1_200_000 {
		t.Errorf("original not replaced in place: size %d, want 1200000", st.Size())
	}
}

func TestAC3ValidTrackSkips(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "valid.mkv")
	writeFile(t, src, 1000)

	validJSON := fmt.Sprintf(`{"streams":[%s,%s],"format":{"duration":"600.0","size":"1000","bit_rate":"1"}}`,
		audioJSON("dts", "eng", "", "1536000", 6),
		audioJSON("ac3", "eng", "", "640000", 6))
	ff := &fakeFF{probeFor: func(string) (string, bool) { return validJSON, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000}, out)
	tl.processFile(1, 1, src)

	if tl.untouched != 1 || !strings.Contains(out.String(), "OK (nothing to convert)") {
		t.Errorf("valid AC3 must skip conversion: %+v out=%s", tl, out.String())
	}
}

func TestAC3LowBitrateInvalid(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "low.mkv")
	writeFile(t, src, 1000)

	lowJSON := fmt.Sprintf(`{"streams":[%s,%s],"format":{"duration":"600.0","size":"1000","bit_rate":"1"}}`,
		audioJSON("dts", "eng", "", "1536000", 6),
		audioJSON("ac3", "eng", "", "192000", 2)) // low bitrate, no commentary title
	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(3, 2), true
		}
		return lowJSON, true
	}, encSize: 1}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000}, &bytes.Buffer{})
	tl.processFile(1, 1, src)

	if tl.converted != 1 {
		t.Fatalf("low-bitrate AC3 must not count as valid: %+v", tl)
	}
	if !hasPair(ff.encodeLog[0], "-map", "0:a:0") || !hasPair(ff.encodeLog[0], "-c:a:2", "ac3") {
		// srcTotal=2 -> the appended copy is output audio ordinal 2
		t.Errorf("dts track must be appended as ac3: %v", ff.encodeLog[0])
	}
}

func TestAC3Gate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1000)

	ff := &fakeFF{probeFor: func(string) (string, bool) { return mixedJSON(), true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000, GateLang: "rus"}, out)
	tl.processFile(1, 1, src)

	if tl.untouched != 1 || !strings.Contains(out.String(), "no 'rus' audio track") {
		t.Errorf("language gate must skip: %+v out=%s", tl, out.String())
	}
}

func TestAC3EAC3NeverConverted(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "eac3.mkv")
	writeFile(t, src, 1000)

	eac3JSON := fmt.Sprintf(`{"streams":[%s,%s],"format":{"duration":"600.0","size":"1000","bit_rate":"1"}}`,
		videoJSON(),
		audioJSON("eac3", "ger", "", "768000", 6))
	ff := &fakeFF{probeFor: func(string) (string, bool) { return eac3JSON, true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000}, out)
	tl.processFile(1, 1, src)

	if tl.untouched != 1 || !strings.Contains(out.String(), "OK (nothing to convert)") {
		t.Errorf("E-AC3 must never be converted: %+v out=%s", tl, out.String())
	}
}

func TestAC3DryRun(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(string) (string, bool) { return mixedJSON(), true }}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000, DryRun: true}, out)
	tl.processFile(1, 1, src)

	if tl.converted != 1 || len(ff.encodeLog) != 0 {
		t.Fatalf("dry run must not encode: %+v", tl)
	}
	for _, want := range []string{
		"Would convert",
		"append AC3 640k for audio #0 (dts, lang 'eng')",
		"append AC3 640k for audio #2 (truehd, lang 'ces')",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan missing %q: %s", want, out.String())
		}
	}
}

func TestAC3CountGate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	writeFile(t, src, 1_000_000)

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(3, 2), true // one appended track missing
		}
		return mixedJSON(), true
	}, encSize: 1}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000}, out)
	tl.processFile(1, 1, src)

	if tl.failed != 1 || !strings.Contains(out.String(), "does not match the plan") {
		t.Errorf("count gate must reject: %+v out=%s", tl, out.String())
	}
}

func TestAC3RunAllWithIgnores(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mkv")
	b := filepath.Join(dir, "b.mkv")
	writeFile(t, a, 1000)
	writeFile(t, b, 1000)
	if err := os.WriteFile(filepath.Join(dir, ".ac3converter-ignore"), []byte("b.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: func(src string) (string, bool) {
		if strings.HasSuffix(filepath.Base(src), ".tmp") {
			return remuxJSON(5, 3), true
		}
		return mixedJSON(), true
	}, encSize: 1}
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Target: dir, MinBR: 448000}, out)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if len(ff.encodeLog) != 1 || !hasPair(ff.encodeLog[0], "-i", a) {
		t.Errorf("only a.mkv must convert: %v", ff.encodeLog)
	}
	if !strings.Contains(out.String(), "Skipping (ignored)") {
		t.Errorf("ignore skip line missing: %s", out.String())
	}
}

func TestPlanCompat(t *testing.T) {
	info := &probe.Info{Streams: []probe.Stream{
		{CodecName: "dts", CodecType: "audio", Channels: 6, Tags: probe.Tags{Language: "cs"}},
		// en has a low-bitrate AC3 (192k < 448k) plus a valid 640k one -> valid
		{CodecName: "ac3", CodecType: "audio", BitRate: "192000", Tags: probe.Tags{Language: "en"}},
		{CodecName: "ac3", CodecType: "audio", BitRate: "640000", Tags: probe.Tags{Language: "en"}},
		{CodecName: "truehd", CodecType: "audio", Tags: probe.Tags{Language: "en"}},
		// commentary AC3 never validates a language
		{CodecName: "dts", CodecType: "audio", Channels: 6, Tags: probe.Tags{Language: "de"}},
		{CodecName: "ac3", CodecType: "audio", BitRate: "640000", Tags: probe.Tags{Language: "de", Title: "Director commentary"}},
		{CodecName: "flac", CodecType: "audio", Tags: probe.Tags{Language: "sk"}},
	}}

	plans := PlanCompat(info, 448000)
	if len(plans) != 3 {
		t.Fatalf("plans: %+v", plans)
	}
	want := []PlanEntry{
		{Ordinal: 0, Lang: "cs", Codec: "dts"},
		{Ordinal: 4, Lang: "de", Codec: "dts"},
		{Ordinal: 6, Lang: "sk", Codec: "flac"},
	}
	for i, w := range want {
		if plans[i] != w {
			t.Errorf("plans[%d] = %+v, want %+v", i, plans[i], w)
		}
	}

	if total, ac3Count := CountTracks(info); total != 7 || ac3Count != 3 {
		t.Errorf("CountTracks = %d/%d, want 7/3", total, ac3Count)
	}
}
