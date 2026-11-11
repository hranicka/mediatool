package encoders

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/run"
)

type fakeRunner struct {
	// failEncs fails every probe whose -c:v value is a key
	failEncs map[string]bool
	lastArgs []string
}

func (f *fakeRunner) Output(name string, args ...string) ([]byte, error) {
	f.lastArgs = args
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c:v" && f.failEncs[args[i+1]] {
			return nil, errors.New("probe fail")
		}
	}
	return []byte{}, nil
}

func (f *fakeRunner) Stream(name string, args ...string) ([]byte, error) {
	return f.Output(name, args...)
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func TestQualityArgs(t *testing.T) {
	cases := []struct {
		codec Codec
		kind  Kind
		want  []string
	}{
		{CodecHEVC, NVENC, []string{"-c:v", "hevc_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "26", "-b:v", "0", "-spatial-aq", "1", "-temporal-aq", "1", "-rc-lookahead", "32", "-multipass", "2"}},
		{CodecHEVC, QSV, []string{"-c:v", "hevc_qsv", "-preset", "medium", "-global_quality", "26"}},
		{CodecHEVC, VAAPI, []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", "26"}},
		{CodecHEVC, SW, []string{"-c:v", "libx265", "-preset", "medium", "-crf", "26"}},
		{CodecAV1, NVENC, []string{"-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "26", "-b:v", "0", "-spatial-aq", "1", "-temporal-aq", "1", "-rc-lookahead", "32", "-multipass", "2"}},
		{CodecAV1, QSV, []string{"-c:v", "av1_qsv", "-preset", "medium", "-global_quality", "26"}},
		{CodecAV1, VAAPI, []string{"-c:v", "av1_vaapi", "-rc_mode", "CQP", "-qp", "26"}},
		{CodecAV1, SW, []string{"-c:v", "libsvtav1", "-preset", "8", "-crf", "26"}},
	}
	for _, c := range cases {
		e := Encoder{Codec: c.codec, Kind: c.kind}
		if got := e.VideoArgs(ModeMovie, 26, 0); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s/%s movie: got %v, want %v", c.codec, c.kind, got, c.want)
		}
		// an unknown source bitrate in capped modes degrades to quality
		// targeting (bash parity)
		if got := e.VideoArgs(ModeCamera, 26, 0); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s/%s camera without source bitrate: got %v, want %v", c.codec, c.kind, got, c.want)
		}
	}
}

func TestCappedArgs(t *testing.T) {
	// source 2000 kbps: camera targets 1100, caps at 1300
	nvenc := Encoder{Codec: CodecHEVC, Kind: NVENC}
	want := []string{"-c:v", "hevc_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "28", "-b:v", "0", "-spatial-aq", "1", "-temporal-aq", "1", "-rc-lookahead", "32", "-multipass", "2", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := nvenc.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("nvenc camera: got %v, want %v", got, want)
	}

	qsv := Encoder{Codec: CodecHEVC, Kind: QSV}
	want = []string{"-c:v", "hevc_qsv", "-preset", "medium", "-rc", "vbr", "-b:v", "1100k", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := qsv.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("qsv camera: got %v, want %v", got, want)
	}

	vaapi := Encoder{Codec: CodecHEVC, Kind: VAAPI}
	want = []string{"-c:v", "hevc_vaapi", "-rc_mode", "VBR", "-b:v", "1100k", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := vaapi.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("vaapi camera: got %v, want %v", got, want)
	}

	// lowq: source 2000 -> tgt 800, cap 1000
	x265 := Encoder{Codec: CodecHEVC, Kind: SW}
	want = []string{"-c:v", "libx265", "-preset", "medium", "-crf", "31", "-maxrate", "1000k", "-bufsize", "2000k"}
	if got := x265.VideoArgs(ModeLowQ, 31, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("x265 lowq: got %v, want %v", got, want)
	}

	// av1 variants keep the same cap math
	av1NVENC := Encoder{Codec: CodecAV1, Kind: NVENC}
	want = []string{"-c:v", "av1_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "28", "-b:v", "0", "-spatial-aq", "1", "-temporal-aq", "1", "-rc-lookahead", "32", "-multipass", "2", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := av1NVENC.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("av1 nvenc camera: got %v, want %v", got, want)
	}

	av1SW := Encoder{Codec: CodecAV1, Kind: SW}
	want = []string{"-c:v", "libsvtav1", "-preset", "8", "-crf", "31", "-maxrate", "1000k", "-bufsize", "2000k"}
	if got := av1SW.VideoArgs(ModeLowQ, 31, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("libsvtav1 lowq: got %v, want %v", got, want)
	}
}

func TestCappedPlan(t *testing.T) {
	// sources whose cap would fall below the sane minimum get no cap at
	// all (pure quality targeting, size gate as backstop)
	if tgt, cap := cappedPlan(ModeCamera, 100); tgt != 0 || cap != 0 {
		t.Errorf("camera tiny source: got %d/%d, want 0/0", tgt, cap)
	}
	if tgt, cap := cappedPlan(ModeLowQ, 1199); tgt != 0 || cap != 0 {
		t.Errorf("lowq below threshold: got %d/%d, want 0/0", tgt, cap)
	}
	// above the threshold the percentages are exact
	if tgt, cap := cappedPlan(ModeCamera, 2000); tgt != 1100 || cap != 1300 {
		t.Errorf("camera: got %d/%d, want 1100/1300", tgt, cap)
	}
	if tgt, cap := cappedPlan(ModeLowQ, 2000); tgt != 800 || cap != 1000 {
		t.Errorf("lowq: got %d/%d, want 800/1000", tgt, cap)
	}
}

func TestChromecastArgs(t *testing.T) {
	nvenc := Encoder{Codec: CodecHEVC, Kind: NVENC}
	want := []string{"-c:v", "hevc_nvenc", "-preset", "p6", "-tune", "hq", "-cq", "22", "-maxrate", "15M", "-bufsize", "15M", "-profile:v", "main", "-level", "4.1", "-tier", "main"}
	if got := nvenc.VideoArgs(ModeChromecast, 22, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("nvenc chromecast: got %v, want %v", got, want)
	}

	qsv := Encoder{Codec: CodecHEVC, Kind: QSV}
	want = []string{"-c:v", "hevc_qsv", "-preset", "medium", "-global_quality", "22", "-maxrate", "15M", "-bufsize", "15M"}
	if got := qsv.VideoArgs(ModeChromecast, 22, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("qsv chromecast: got %v, want %v", got, want)
	}
}

func TestDefaultCQ(t *testing.T) {
	cases := map[Mode]int{ModeMovie: 26, ModeCamera: 28, ModeLowQ: 31, ModeChromecast: 22}
	for m, want := range cases {
		if got := m.DefaultCQ(); got != want {
			t.Errorf("%s DefaultCQ: got %d, want %d", m, got, want)
		}
	}
}

func TestFiltersAndPixFmt(t *testing.T) {
	vaapi := Encoder{Codec: CodecHEVC, Kind: VAAPI}
	if got := vaapi.Filters(true); !reflect.DeepEqual(got, []string{"format=p010le", "hwupload"}) {
		t.Errorf("vaapi 10-bit filters: got %v", got)
	}
	if got := vaapi.Filters(false); !reflect.DeepEqual(got, []string{"format=nv12", "hwupload"}) {
		t.Errorf("vaapi 8-bit filters: got %v", got)
	}
	if got := vaapi.PixFmtArgs(true); got != nil {
		t.Errorf("vaapi must not get -pix_fmt args, got %v", got)
	}
	if got := vaapi.InputArgs("/dev/dri/renderD128"); !reflect.DeepEqual(got, []string{"-vaapi_device", "/dev/dri/renderD128"}) {
		t.Errorf("vaapi input args: got %v", got)
	}

	x265 := Encoder{Codec: CodecHEVC, Kind: SW}
	if got := x265.Filters(true); got != nil {
		t.Errorf("x265 needs no filters, got %v", got)
	}
	if got := x265.PixFmtArgs(true); !reflect.DeepEqual(got, []string{"-pix_fmt", "p010le"}) {
		t.Errorf("x265 10-bit pix_fmt: got %v", got)
	}
	if got := x265.PixFmtArgs(false); got != nil {
		t.Errorf("8-bit needs no pix_fmt arg, got %v", got)
	}
	if got := x265.InputArgs("/dev/dri/renderD128"); got != nil {
		t.Errorf("x265 needs no input args, got %v", got)
	}
}

func TestLabels(t *testing.T) {
	if got := Label(CodecHEVC, NVENC); got != "Nvidia NVENC" {
		t.Errorf("nvenc label: %q", got)
	}
	if got := Label(CodecHEVC, SW); got != "software libx265" {
		t.Errorf("hevc sw label: %q", got)
	}
	if got := Label(CodecAV1, SW); got != "software libsvtav1" {
		t.Errorf("av1 sw label: %q", got)
	}
}

func TestCandidates(t *testing.T) {
	for _, c := range []Codec{CodecHEVC, CodecAV1} {
		got := Candidates(c)
		if !reflect.DeepEqual(got, []Kind{NVENC, QSV, VAAPI, SW}) {
			t.Errorf("%s candidates: %v", c, got)
		}
	}
}

func TestProbePicksFirstWorking(t *testing.T) {
	r := &fakeRunner{}
	e, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", CodecHEVC, Candidates(CodecHEVC))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if e.Kind != NVENC || e.Codec != CodecHEVC {
		t.Errorf("expected hevc nvenc first, got %s/%s", e.Codec, e.Kind)
	}
	if !contains(r.lastArgs, "-frames:v") || !contains(r.lastArgs, "-f") {
		t.Errorf("probe must run a real test encode, got %v", r.lastArgs)
	}
	if !contains(r.lastArgs, "hevc_nvenc") {
		t.Errorf("hevc probe must test hevc_nvenc, got %v", r.lastArgs)
	}
}

func TestProbeSkipsBroken(t *testing.T) {
	r := &fakeRunner{failEncs: map[string]bool{"hevc_nvenc": true, "hevc_qsv": true}}
	e, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", CodecHEVC, Candidates(CodecHEVC))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if e.Kind != VAAPI {
		t.Errorf("expected vaapi after nvenc/qsv fail, got %s", e.Kind)
	}
}

func TestProbeAllFail(t *testing.T) {
	r := &fakeRunner{failEncs: map[string]bool{"hevc_nvenc": true, "hevc_qsv": true, "hevc_vaapi": true, "libx265": true}}
	if _, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", CodecHEVC, Candidates(CodecHEVC)); err == nil {
		t.Fatal("expected error when every probe fails")
	}
}

func TestProbeAV1UsesAV1Names(t *testing.T) {
	r := &fakeRunner{failEncs: map[string]bool{"av1_nvenc": true}}
	e, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", CodecAV1, Candidates(CodecAV1))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if e.Kind != QSV || e.Codec != CodecAV1 {
		t.Errorf("expected av1 qsv after nvenc fail, got %s/%s", e.Codec, e.Kind)
	}
	if !contains(r.lastArgs, "av1_qsv") {
		t.Errorf("av1 probe must fall through to av1_qsv, got %v", r.lastArgs)
	}
}

func TestProbeVAAPIArgs(t *testing.T) {
	r := &fakeRunner{failEncs: map[string]bool{"hevc_nvenc": true, "hevc_qsv": true}}
	e, _ := Probe(r, "ffmpeg", "/dev/dri/renderD129", CodecHEVC, Candidates(CodecHEVC))
	if e.Kind != VAAPI {
		t.Fatalf("expected vaapi, got %s", e.Kind)
	}
	// the vaapi probe must init the device and upload frames
	args := r.lastArgs
	if !contains(args, "-vaapi_device") || !contains(args, "/dev/dri/renderD129") {
		t.Errorf("vaapi probe args missing device init: %v", args)
	}
	if !contains(args, "-vf") || !contains(args, "format=nv12,hwupload") {
		t.Errorf("vaapi probe args missing hwupload chain: %v", args)
	}
}

func TestParseKindModeAndCodec(t *testing.T) {
	if k, err := ParseKind("vaapi"); err != nil || k != VAAPI {
		t.Errorf("ParseKind vaapi: %v %v", k, err)
	}
	if k, err := ParseKind("sw"); err != nil || k != SW {
		t.Errorf("ParseKind sw: %v %v", k, err)
	}
	// x265 is the deprecated alias for the software backend
	if k, err := ParseKind("x265"); err != nil || k != SW {
		t.Errorf("ParseKind x265: %v %v", k, err)
	}
	if _, err := ParseKind("cuda"); err == nil {
		t.Error("unknown encoder must fail")
	}
	if m, err := ParseMode("lowq"); err != nil || m != ModeLowQ {
		t.Errorf("ParseMode lowq: %v %v", m, err)
	}
	if _, err := ParseMode("3d"); err == nil {
		t.Error("unknown mode must fail")
	}
	if c, err := ParseCodec("av1"); err != nil || c != CodecAV1 {
		t.Errorf("ParseCodec av1: %v %v", c, err)
	}
	if _, err := ParseCodec("vvc"); err == nil {
		t.Error("unknown codec must fail")
	}
}

func TestPlanInfo(t *testing.T) {
	nvenc := Encoder{Codec: CodecHEVC, Kind: NVENC}
	if got := nvenc.PlanInfo(ModeMovie, 26, 0); got != "CQ 26 quality target" {
		t.Errorf("movie plan: %q", got)
	}
	if got := nvenc.PlanInfo(ModeCamera, 28, 2000); got != "CQ 28, capped at 1300k (source 2000k)" {
		t.Errorf("camera plan: %q", got)
	}
	if got := nvenc.PlanInfo(ModeCamera, 28, 0); got != "CQ 28 quality target (no cap)" {
		t.Errorf("camera plan no cap: %q", got)
	}
	if got := nvenc.PlanInfo(ModeChromecast, 22, 0); got != "CQ 22, 15M cap, level 4.1" {
		t.Errorf("chromecast plan: %q", got)
	}
	// the vaapi plan must not claim a bitrate ceiling CQP cannot enforce
	if got := (Encoder{Codec: CodecHEVC, Kind: VAAPI}).PlanInfo(ModeChromecast, 22, 0); strings.Contains(got, "15M cap") {
		t.Errorf("vaapi chromecast plan must not claim a ceiling: %q", got)
	}
}

func TestPlanSummary(t *testing.T) {
	nvenc := Encoder{Codec: CodecHEVC, Kind: NVENC}
	cases := []struct {
		m    Mode
		cq   int
		want string
	}{
		{ModeMovie, 22, "CQ 22 quality target"},
		{ModeCamera, 28, "CQ 28, capped at ~65% of source bitrate"},
		{ModeLowQ, 31, "CQ 31, capped at ~50% of source bitrate"},
		{ModeChromecast, 22, "CQ 22, 15M cap, level 4.1"},
	}
	for _, c := range cases {
		if got := nvenc.PlanSummary(c.m, c.cq); got != c.want {
			t.Errorf("PlanSummary(%s, %d) = %q, want %q", c.m, c.cq, got, c.want)
		}
	}
	// the header summary must not claim a ceiling VAAPI CQP cannot enforce
	if got := (Encoder{Codec: CodecHEVC, Kind: VAAPI}).PlanSummary(ModeChromecast, 22); strings.Contains(got, "15M cap") {
		t.Errorf("vaapi chromecast summary must not claim a ceiling: %q", got)
	}
}

var _ run.Runner = &fakeRunner{}
