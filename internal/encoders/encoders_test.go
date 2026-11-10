package encoders

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hranicka/media-tool/internal/run"
)

type fakeRunner struct {
	failKinds map[Kind]bool
	lastArgs  []string
}

func (f *fakeRunner) Output(name string, args ...string) ([]byte, error) {
	f.lastArgs = args
	for _, k := range []Kind{"nvenc", "qsv", "vaapi", "x265"} {
		if f.failKinds[k] && contains(args, "-c:v") && contains(args, "hevc_"+string(k)) {
			return nil, errors.New("probe fail")
		}
		if f.failKinds[k] && contains(args, "libx265") && k == "x265" {
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
		kind Kind
		want []string
	}{
		{NVENC, []string{"-c:v", "hevc_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "26", "-b:v", "0", "-spatial-aq", "1"}},
		{QSV, []string{"-c:v", "hevc_qsv", "-preset", "medium", "-global_quality", "26"}},
		{VAAPI, []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", "26"}},
		{X265, []string{"-c:v", "libx265", "-preset", "medium", "-crf", "26"}},
	}
	for _, c := range cases {
		e := Encoder{Kind: c.kind, Label: labels[c.kind]}
		if got := e.VideoArgs(ModeMovie, 26, 0); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s movie: got %v, want %v", c.kind, got, c.want)
		}
		// an unknown source bitrate in capped modes degrades to quality
		// targeting (bash parity)
		if got := e.VideoArgs(ModeCamera, 26, 0); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s camera without source bitrate: got %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestCappedArgs(t *testing.T) {
	// source 2000 kbps: camera targets 1100, caps at 1300
	nvenc := Encoder{Kind: NVENC}
	want := []string{"-c:v", "hevc_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "28", "-b:v", "0", "-spatial-aq", "1", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := nvenc.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("nvenc camera: got %v, want %v", got, want)
	}

	qsv := Encoder{Kind: QSV}
	want = []string{"-c:v", "hevc_qsv", "-preset", "medium", "-rc", "vbr", "-b:v", "1100k", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := qsv.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("qsv camera: got %v, want %v", got, want)
	}

	vaapi := Encoder{Kind: VAAPI}
	want = []string{"-c:v", "hevc_vaapi", "-rc_mode", "VBR", "-b:v", "1100k", "-maxrate", "1300k", "-bufsize", "2600k"}
	if got := vaapi.VideoArgs(ModeCamera, 28, 2000); !reflect.DeepEqual(got, want) {
		t.Errorf("vaapi camera: got %v, want %v", got, want)
	}

	// lowq with floors: source 1000 -> tgt 400 (at floor), cap 600 (floor)
	x265 := Encoder{Kind: X265}
	want = []string{"-c:v", "libx265", "-preset", "medium", "-crf", "31", "-maxrate", "600k", "-bufsize", "1200k"}
	if got := x265.VideoArgs(ModeLowQ, 31, 1000); !reflect.DeepEqual(got, want) {
		t.Errorf("x265 lowq: got %v, want %v", got, want)
	}
}

func TestCappedPlanFloors(t *testing.T) {
	// tiny source bitrates must hit the floors
	tgt, cap := cappedPlan(ModeCamera, 100)
	if tgt != 500 || cap != 800 {
		t.Errorf("camera floor: got tgt=%d cap=%d, want 500/800", tgt, cap)
	}
	tgt, cap = cappedPlan(ModeLowQ, 100)
	if tgt != 400 || cap != 600 {
		t.Errorf("lowq floor: got tgt=%d cap=%d, want 400/600", tgt, cap)
	}
}

func TestChromecastArgs(t *testing.T) {
	nvenc := Encoder{Kind: NVENC}
	want := []string{"-c:v", "hevc_nvenc", "-preset", "p6", "-tune", "hq", "-cq", "22", "-maxrate", "15M", "-bufsize", "15M", "-profile:v", "main", "-level", "4.1", "-tier", "main"}
	if got := nvenc.VideoArgs(ModeChromecast, 22, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("nvenc chromecast: got %v, want %v", got, want)
	}

	qsv := Encoder{Kind: QSV}
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
	vaapi := Encoder{Kind: VAAPI}
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

	x265 := Encoder{Kind: X265}
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

func TestProbePicksFirstWorking(t *testing.T) {
	r := &fakeRunner{}
	e, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", Candidates())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if e.Kind != NVENC {
		t.Errorf("expected nvenc first, got %s", e.Kind)
	}
	if !contains(r.lastArgs, "-frames:v") || !contains(r.lastArgs, "-f") {
		t.Errorf("probe must run a real test encode, got %v", r.lastArgs)
	}
}

func TestProbeSkipsBroken(t *testing.T) {
	r := &fakeRunner{failKinds: map[Kind]bool{NVENC: true, QSV: true}}
	e, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", Candidates())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if e.Kind != VAAPI {
		t.Errorf("expected vaapi after nvenc/qsv fail, got %s", e.Kind)
	}
}

func TestProbeAllFail(t *testing.T) {
	r := &fakeRunner{failKinds: map[Kind]bool{NVENC: true, QSV: true, VAAPI: true, X265: true}}
	if _, err := Probe(r, "ffmpeg", "/dev/dri/renderD128", Candidates()); err == nil {
		t.Fatal("expected error when every probe fails")
	}
}

func TestProbeVAAPIArgs(t *testing.T) {
	r := &fakeRunner{failKinds: map[Kind]bool{NVENC: true, QSV: true}}
	e, _ := Probe(r, "ffmpeg", "/dev/dri/renderD129", Candidates())
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

func TestParseKindAndMode(t *testing.T) {
	if k, err := ParseKind("vaapi"); err != nil || k != VAAPI {
		t.Errorf("ParseKind vaapi: %v %v", k, err)
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
}

func TestPlanInfo(t *testing.T) {
	if got := ModeMovie.PlanInfo(26, 0); got != "CQ 26 quality target" {
		t.Errorf("movie plan: %q", got)
	}
	if got := ModeCamera.PlanInfo(28, 2000); got != "CQ 28, capped at 1300k (source 2000k)" {
		t.Errorf("camera plan: %q", got)
	}
	if got := ModeCamera.PlanInfo(28, 0); got != "CQ 28 quality target (source bitrate unknown, no cap)" {
		t.Errorf("camera plan unknown bitrate: %q", got)
	}
}

var _ run.Runner = &fakeRunner{}
