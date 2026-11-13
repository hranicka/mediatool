package align

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

type fakeFF struct {
	probeFor   func(src string) (string, bool)
	framesFor  func(src string) []byte
	rawFor     func(src string) []byte
	pcmFor     func(src string) []byte
	streamLog  [][]string
	frameCalls int
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
			if args[i] == "-f" && args[i+1] == "f32le" {
				// the calibration's audio decode is not a frame extraction
				if f.pcmFor != nil {
					return f.pcmFor(args[len(args)-1]), nil
				}
				return []byte{}, nil
			}
		}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-i" && f.framesFor != nil {
				f.frameCalls++
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
	if err := tl.runOne(); err != nil {
		t.Fatalf("runAll: %v (out: %s)", err, out.String())
	}

	got := out.String()
	if !strings.Contains(got, "Timing: speed 1.00000 (same speed), 1 segment(s)") {
		t.Errorf("missing timing line: %s", got)
	}
	// the unmatched reference head is reported as a silent span
	if !strings.Contains(got, "silent head: 0:00:02 of the reference has no dub audio") {
		t.Errorf("missing silent head line: %s", got)
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
		{"-map", "0"},     // every reference stream survives
		{"-map", "1:a:0"}, // the appended dub track
		{"-c", "copy"},
		{"-c:a:1", "aac"},
		{"-b:a:1", "192k"},
		{"-metadata:s:a:1", "language=ces"},
		{"-metadata:s:a:1", "title=Dubbed (resampled)"},
		{"-disposition:a:1", "0"},
		{"-map_metadata", "0"},
		{"-f", "matroska"},
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
	if err := tl.runOne(); err == nil {
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
	if err := tl.runOne(); err == nil {
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
	if err := tl.runOne(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Extra         : DRY RUN") {
		t.Errorf("missing dry-run header: %s", got)
	}
	if !strings.Contains(got, "Timing fitted") {
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
	if err := tl.runOne(); err == nil {
		t.Fatal("expected an error for a dub without audio")
	} else if !strings.Contains(err.Error(), "no audio track") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAudioLead(t *testing.T) {
	// the dubbed audio starts 0.3 s after the first video frame
	info := &probe.Info{
		Format: probe.Format{StartTime: "1.400000"},
		Streams: []probe.Stream{
			{CodecType: probe.TypeVideo, StartTime: "1.400000"},
			{CodecType: probe.TypeAudio, StartTime: "1.700000"},
		},
	}
	if got := audioLead(info, probe.Typed{Stream: info.Streams[1]}); math.Abs(got-0.3) > 1e-9 {
		t.Errorf("lead: got %v, want 0.3", got)
	}
	// audio at the video start (the normal case) needs no correction
	info.Streams[1].StartTime = "1.400000"
	if got := audioLead(info, probe.Typed{Stream: info.Streams[1]}); got != 0 {
		t.Errorf("lead: got %v, want 0", got)
	}
	// the container may start earlier than the video (e.g. a subtitle
	// stream), and cover art must not be mistaken for the video: the lead
	// is measured from the first real video frame
	info = &probe.Info{
		Format: probe.Format{StartTime: "0.000000"},
		Streams: []probe.Stream{
			{CodecType: probe.TypeVideo, CodecName: "mjpeg", StartTime: "0.000000", Disposition: probe.Disposition{AttachedPic: 1}},
			{CodecType: probe.TypeSubtitle, StartTime: "0.000000"},
			{CodecType: probe.TypeVideo, StartTime: "1.000000"},
			{CodecType: probe.TypeAudio, StartTime: "1.500000"},
		},
	}
	if got := audioLead(info, probe.Typed{Stream: info.Streams[3]}); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("lead from video start: got %v, want 0.5", got)
	}
	// a missing video start falls back to the container origin
	info.Streams[2].StartTime = ""
	if got := audioLead(info, probe.Typed{Stream: info.Streams[3]}); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("lead with container fallback: got %v, want 1.5", got)
	}
	// implausible metadata is ignored
	info.Streams[3].StartTime = "99"
	if got := audioLead(info, probe.Typed{Stream: info.Streams[3]}); got != 0 {
		t.Errorf("implausible lead: got %v, want 0", got)
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
	if err := tl.runOne(); err == nil {
		t.Fatal("expected an error for unrelated content")
	} else if !strings.Contains(err.Error(), "too few reliable matches") {
		t.Errorf("unexpected error: %v", err)
	}
}
