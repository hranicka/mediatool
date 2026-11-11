package align

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

type fakeFF struct {
	probeFor  func(src string) (string, bool)
	framesFor func(src string) []byte
	rawFor    func(src string) []byte
	streamLog [][]string
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
			if args[i] == "-i" && f.framesFor != nil {
				return f.framesFor(args[i+1]), nil
			}
		}
		return []byte{}, nil
	}
	return []byte{}, nil
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	f.streamLog = append(f.streamLog, args)
	last := args[len(args)-1]
	data := []byte("muxed")
	if f.rawFor != nil {
		data = f.rawFor(last)
	}
	return nil, os.WriteFile(last, data, 0o644)
}

const refJSON = `{"streams":[` +
	`{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
	`{"index":1,"codec_name":"ac3","codec_type":"audio"},` +
	`{"index":2,"codec_name":"subrip","codec_type":"subtitle"}],"format":{"duration":"20.0","size":"1000","bit_rate":"1"}}`

const dubJSON = `{"streams":[` +
	`{"index":0,"codec_name":"mpeg2video","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
	`{"index":1,"codec_name":"mp2","codec_type":"audio"}],"format":{"duration":"18.0","size":"1000","bit_rate":"1"}}`

const noAudioJSON = `{"streams":[{"index":0,"codec_name":"mpeg2video","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}}],"format":{"duration":"18.0","size":"1000","bit_rate":"1"}}`

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestTool(t *testing.T, dir string, ff run.Runner, cfg *Config, out *bytes.Buffer) *tool {
	t.Helper()
	return &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		cfg:        cfg,
		rep:        ui.New(out, ui.Profile{}),
		guard:      &run.TmpGuard{},
	}
}

// alignFixture builds ref/dub files carrying the same frames: the dub is
// the reference content starting 10 frames (2 s at interval 0.2) later.
func alignFixture(t *testing.T) (dir, ref, dub string, ff *fakeFF) {
	t.Helper()
	dir = t.TempDir()
	ref = filepath.Join(dir, "ref.mkv")
	dub = filepath.Join(dir, "dub.ts")
	writeFile(t, ref, 1000)
	writeFile(t, dub, 1000)

	frameSeeds := func(start, count int) []byte {
		buf := make([]byte, 0, count*vhash.FrameLen)
		for t := start; t < start+count; t++ {
			var h [vhash.FrameLen]byte
			rng := rand.New(rand.NewSource(int64(t)))
			rng.Read(h[:])
			buf = append(buf, h[:]...)
		}
		return buf
	}

	ff = &fakeFF{probeFor: func(src string) (string, bool) {
		if filepath.Base(src) == "ref.mkv" {
			return refJSON, true
		}
		if filepath.Base(src) == "dub.ts" {
			return dubJSON, true
		}
		return "", false
	}, framesFor: func(src string) []byte {
		if filepath.Base(src) == "ref.mkv" {
			return frameSeeds(0, 100)
		}
		if filepath.Base(src) == "dub.ts" {
			return frameSeeds(10, 90)
		}
		return nil
	}, rawFor: func(src string) []byte {
		if strings.HasSuffix(src, "dub.raw") {
			return dubRaw(18 * sampleRate)
		}
		return []byte("muxed output")
	}}
	return dir, ref, dub, ff
}

func TestAlignRunAll(t *testing.T) {
	dir, ref, dub, ff := alignFixture(t)
	outPath := filepath.Join(dir, "ref+dub.mkv")

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Ref: ref, Dub: dub, Out: outPath, Interval: 0.2, MatchDist: 45, Bitrate: "192k", LangRef: "eng", LangDub: "ces"}, out)
	defer tl.guard.Cleanup()
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v (out: %s)", err, out.String())
	}

	got := out.String()
	if !strings.Contains(got, "Timing map: 90 reliable matches, 0 outliers dropped") {
		t.Errorf("missing timing map line: %s", got)
	}
	// the +2 s shift must show up at both ends of the map
	if !strings.Contains(got, "dub 0s -> ref 2.0s; dub 18s -> ref 20.0s") {
		t.Errorf("missing map endpoints: %s", got)
	}
	if !strings.Contains(got, "Created: "+outPath) {
		t.Errorf("missing created line: %s", got)
	}

	// the mux is the second ffmpeg Stream call, after the dub extraction
	if len(ff.streamLog) != 2 {
		t.Fatalf("expected dub extraction + mux, got %d stream calls", len(ff.streamLog))
	}
	args := ff.streamLog[1]
	want := [][2]string{
		{"-i", ref},
		{"-map", "0:v:0"},
		{"-map", "0:a:0"},
		{"-map", "0:s:0"}, // the reference subtitle survives
		{"-map", "1:a:0"},
		{"-c:v", "copy"},
		{"-c:a:0", "copy"},
		{"-c:a:1", "aac"},
		{"-b:a:1", "192k"},
		{"-metadata:s:a:0", "language=eng"},
		{"-metadata:s:a:1", "language=ces"},
	}
	for _, pair := range want {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == pair[0] && args[i+1] == pair[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("mux args missing %q %q: %v", pair[0], pair[1], args)
		}
	}
	if args[len(args)-1] != outPath {
		t.Errorf("mux output must be %s, got %s", outPath, args[len(args)-1])
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("output file missing: %v", err)
	}
}

func TestAlignFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"-interval", "0", "a", "b"},
		{"-match-dist", "0", "a", "b"},
		{"-match-dist", "1025", "a", "b"},
	} {
		if err := Run(args); err == nil {
			t.Errorf("Run(%v) must fail validation", args)
		}
	}
}

func TestAlignReferenceWithoutAudio(t *testing.T) {
	dir, ref, dub, ff := alignFixture(t)
	videoOnly := `{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}}],"format":{"duration":"20.0","size":"1000","bit_rate":"1"}}`
	ff.probeFor = func(src string) (string, bool) {
		if filepath.Base(src) == "ref.mkv" {
			return videoOnly, true
		}
		return dubJSON, true
	}
	calls := 0
	ff.framesFor = func(src string) []byte { calls++; return nil }

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Ref: ref, Dub: dub, Out: filepath.Join(dir, "out.mkv")}, out)
	defer tl.guard.Cleanup()
	if err := tl.runAll(); err == nil {
		t.Fatal("expected an error for a reference without audio")
	} else if !strings.Contains(err.Error(), "no audio track to copy") {
		t.Errorf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Errorf("must fail before frame extraction, got %d extractions", calls)
	}
}

func TestAlignMissingDuration(t *testing.T) {
	dir, ref, dub, ff := alignFixture(t)
	noDur := `{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},{"index":1,"codec_name":"ac3","codec_type":"audio"}],"format":{"size":"1000","bit_rate":"1"}}`
	ff.probeFor = func(src string) (string, bool) {
		if filepath.Base(src) == "ref.mkv" {
			return noDur, true
		}
		return dubJSON, true
	}

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Ref: ref, Dub: dub, Out: filepath.Join(dir, "out.mkv")}, out)
	defer tl.guard.Cleanup()
	if err := tl.runAll(); err == nil {
		t.Fatal("expected an error for a missing reference duration")
	} else if !strings.Contains(err.Error(), "cannot determine the reference duration") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAlignDryRun(t *testing.T) {
	dir, ref, dub, ff := alignFixture(t)
	outPath := filepath.Join(dir, "ref+dub.mkv")

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Ref: ref, Dub: dub, Out: outPath, Interval: 0.2, MatchDist: 45, Bitrate: "192k", LangRef: "eng", LangDub: "ces", DryRun: true}, out)
	defer tl.guard.Cleanup()
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Extra         : DRY RUN") {
		t.Errorf("missing dry-run header: %s", got)
	}
	if !strings.Contains(got, "Timing map built") {
		t.Errorf("missing dry-run summary: %s", got)
	}
	if len(ff.streamLog) != 0 {
		t.Errorf("dry run must not mux or extract audio: %v", ff.streamLog)
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("dry run must not create the output")
	}
}

func TestAlignDubWithoutAudio(t *testing.T) {
	dir, ref, dub, ff := alignFixture(t)
	ff.probeFor = func(src string) (string, bool) {
		if filepath.Base(src) == "ref.mkv" {
			return refJSON, true
		}
		return noAudioJSON, true
	}

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Ref: ref, Dub: dub, Out: filepath.Join(dir, "out.mkv")}, out)
	defer tl.guard.Cleanup()
	if err := tl.runAll(); err == nil {
		t.Fatal("expected an error for a dub without audio")
	} else if !strings.Contains(err.Error(), "no audio track") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAlignTooFewMatches(t *testing.T) {
	dir, ref, dub, ff := alignFixture(t)
	// the dub carries unrelated content: no reliable matches at all
	ff.framesFor = func(src string) []byte {
		if filepath.Base(src) == "ref.mkv" {
			buf := make([]byte, 100*vhash.FrameLen)
			rand.New(rand.NewSource(1)).Read(buf)
			return buf
		}
		buf := make([]byte, 90*vhash.FrameLen)
		rand.New(rand.NewSource(2)).Read(buf)
		return buf
	}

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, &Config{Ref: ref, Dub: dub, Out: filepath.Join(dir, "out.mkv"), Interval: 0.2, MatchDist: 45}, out)
	defer tl.guard.Cleanup()
	if err := tl.runAll(); err == nil {
		t.Fatal("expected an error for unrelated content")
	} else if !strings.Contains(err.Error(), "too few reliable matches") {
		t.Errorf("unexpected error: %v", err)
	}
}
