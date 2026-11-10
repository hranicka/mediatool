package probe

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hranicka/media-tool/internal/run"
)

type fakeRunner struct {
	out []byte
	err error
}

func (f fakeRunner) Output(_ string, _ ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func (f fakeRunner) Stream(name string, args ...string) ([]byte, error) {
	return f.Output(name, args...)
}

const sampleJSON = `{
  "streams": [
    {"index":0,"codec_name":"h264","codec_type":"video","pix_fmt":"yuv420p10le","width":3840,"height":2160,
     "r_frame_rate":"60000/1001","color_primaries":"bt2020","color_trc":"smpte2084","color_space":"bt2020nc",
     "disposition":{"attached_pic":0},
     "side_data_list":[{"side_data_type":"DOVI configuration record"},{"side_data_type":"HDR10+ dynamic metadata (SMPTE ST 2094-40)"}]},
    {"index":1,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}},
    {"index":2,"codec_name":"dts","codec_type":"audio","channels":6,"bit_rate":"1536000","tags":{"language":"eng","title":"Director's Cut"}},
    {"index":3,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"cze"}}
  ],
  "format": {"duration":"600.5","size":"12345678","bit_rate":"8000000"}
}`

func TestProbeParsesStreams(t *testing.T) {
	info, err := Probe(fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")
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
	_, err := Probe(fakeRunner{err: errors.New("boom")}, "ffprobe", "x.mkv")
	if err == nil {
		t.Fatal("expected error for failing runner")
	}
}

func TestByTypeOrdinals(t *testing.T) {
	info, _ := Probe(fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")
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
	info, _ := Probe(fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")

	if d, ok := info.Duration(); !ok || d != 600.5 {
		t.Errorf("Duration: got %v %v", d, ok)
	}
	if s, ok := info.Size(); !ok || s != 12345678 {
		t.Errorf("Size: got %v %v", s, ok)
	}
}

func TestSideDataDetection(t *testing.T) {
	info, _ := Probe(fakeRunner{out: []byte(sampleJSON)}, "ffprobe", "movie.mkv")

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

var _ run.Runner = fakeRunner{}
