package probe

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hranicka/mediatool/internal/run"
)

type fakeRunner struct {
	out  []byte
	err  error
	name string
	args []string
}

func (f *fakeRunner) Output(name string, args ...string) ([]byte, error) {
	f.name, f.args = name, args
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func (f *fakeRunner) Stream(name string, args ...string) ([]byte, error) {
	return f.Output(name, args...)
}

const sampleJSON = `{
  "streams": [
    {"index":0,"codec_name":"h264","codec_type":"video","pix_fmt":"yuv420p10le","width":3840,"height":2160,
     "r_frame_rate":"60000/1001","color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc",
     "disposition":{"attached_pic":0},
     "side_data_list":[{"side_data_type":"DOVI configuration record"},{"side_data_type":"HDR10+ dynamic metadata (SMPTE ST 2094-40)"}]},
    {"index":1,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}},
    {"index":2,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"eng","title":"Director's Cut"}},
    {"index":3,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"cze"}}
  ],
  "format": {"duration":"600.5","size":"12345678","bit_rate":"8000000"}
}`

func TestProbeParsesStreams(t *testing.T) {
	info, err := Probe(&fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(info.Streams) != 4 {
		t.Fatalf("streams: got %d, want 4", len(info.Streams))
	}
	v := info.Streams[0]
	if v.PixFmt != "yuv420p10le" || v.ColorTransfer != "smpte2084" || v.Height != 2160 {
		t.Errorf("video stream fields not parsed: %+v", v)
	}
	if info.Streams[2].Tags.Title != "Director's Cut" {
		t.Errorf("tags not parsed: %+v", info.Streams[2])
	}
}

func TestProbeError(t *testing.T) {
	_, err := Probe(&fakeRunner{err: errors.New("boom")}, "ffprobe", "x.mkv")
	if err == nil {
		t.Fatal("expected error for failing runner")
	}
}

func TestByTypeOrdinals(t *testing.T) {
	info, _ := Probe(&fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")
	byType := info.ByType()

	if got := byType[TypeVideo]; len(got) != 2 || got[0].Ordinal != 0 || got[1].Ordinal != 1 {
		t.Errorf("video ordinals wrong: %+v", got)
	}
	if got := byType[TypeAudio]; len(got) != 1 || got[0].Stream.CodecName != "dts" {
		t.Errorf("audio grouping wrong: %+v", got)
	}
	if got := byType[TypeSubtitle]; len(got) != 1 || got[0].Stream.Tags.Language != "cze" {
		t.Errorf("subtitle grouping wrong: %+v", got)
	}
}

func TestFormatHelpers(t *testing.T) {
	info, _ := Probe(&fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")

	if d, ok := info.Duration(); !ok || d != 600.5 {
		t.Errorf("Duration: got %v %v", d, ok)
	}
	if s, ok := info.Size(); !ok || s != 12345678 {
		t.Errorf("Size: got %v %v", s, ok)
	}
}

func TestSideDataDetection(t *testing.T) {
	info, _ := Probe(&fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")

	if !info.IsDolbyVision() {
		t.Error("Dolby Vision not detected")
	}
	if !info.IsHDR10Plus() {
		t.Error("HDR10+ not detected")
	}
	if info.Streams[0].Is10Bit() == false || info.Streams[1].Is10Bit() != false {
		t.Error("Is10Bit wrong")
	}
}

func TestByTypeEmpty(t *testing.T) {
	info := &Info{}
	if got := info.ByType(); len(got) != 0 {
		t.Errorf("empty info should group to nothing, got %v", reflect.ValueOf(got).MapKeys())
	}
}

// ffprobe regularly omits optional objects (no tags, no disposition) and
// sends an explicit null side_data_list; parsing must tolerate both.
const minimalJSON = `{
  "streams": [
    {"index":0,"codec_name":"h264","codec_type":"video","side_data_list":null},
    {"index":1,"codec_name":"aac","codec_type":"audio","bit_rate":"","sample_rate":"48000"}
  ],
  "format": {"duration":"N/A","size":"","bit_rate":"","format_name":"matroska,webm"}
}`

func TestProbeToleratesMissingObjects(t *testing.T) {
	info, err := Probe(&fakeRunner{out: []byte(minimalJSON)}, "ffprobe", "x.mkv")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.IsDolbyVision() || info.IsHDR10Plus() {
		t.Error("no side data expected")
	}
	if _, ok := info.Duration(); ok {
		t.Error("'N/A' duration must report false")
	}
	if _, ok := info.Size(); ok {
		t.Error("empty size must report false")
	}
	if info.Streams[1].BitRateInt() != 0 {
		t.Errorf("empty bit_rate must parse to 0, got %d", info.Streams[1].BitRateInt())
	}
	if info.Format.FormatName != "matroska,webm" {
		t.Errorf("format_name not parsed: %q", info.Format.FormatName)
	}
}

func TestRFrameRateInt(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"25/1", 25},
		{"30000/1001", 30000.0 / 1001.0},
		{"25", 25}, // no denominator
		{"0/0", 0}, // ffprobe "unknown"
		{"", 0},
		{"garbage", 0},
		{"-25/1", 0},
		{"10/0", 0},
	}
	for _, c := range cases {
		if got := (Stream{RFrameRate: c.in}).RFrameRateInt(); got != c.want {
			t.Errorf("RFrameRateInt(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestProbeArgs(t *testing.T) {
	r := &fakeRunner{out: []byte(minimalJSON)}
	_, _ = Probe(r, "ffprobe", "x.mkv")

	if r.name != "ffprobe" {
		t.Errorf("binary: got %s", r.name)
	}
	for _, want := range []string{"-print_format", "json", "-show_format", "-show_streams", "x.mkv"} {
		found := false
		for _, a := range r.args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("probe args missing %q: %v", want, r.args)
		}
	}
}

var _ run.Runner = &fakeRunner{}
