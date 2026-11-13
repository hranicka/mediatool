package align

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// latticeNoise is a deterministic band-limited pseudo-noise function of
// time: unique over hours and identical at any sampling rate after
// decimation, so the 8 kHz reference PCM and the 48 kHz dub PCM carry the
// exact same signal and their correlation is unambiguous.
func latticeNoise(t float64) float64 {
	const lattice = 200.0
	x := t * lattice
	i := math.Floor(x)
	f := x - i
	return (hash01(i)*(1-f) + hash01(i+1)*f) * 2
}

func hash01(n float64) float64 {
	v := math.Sin(n*127.1+311.7) * 43758.5453
	return v - math.Floor(v)
}

func patternPCM(rate int, seconds float64, shift float64) []float32 {
	n := int(float64(rate) * seconds)
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(latticeNoise(float64(i)/float64(rate) - shift))
	}
	return out
}

func f32le(pcm []float32) []byte {
	out := make([]byte, len(pcm)*4)
	for i, v := range pcm {
		bits := math.Float32bits(v)
		out[i*4] = byte(bits)
		out[i*4+1] = byte(bits >> 8)
		out[i*4+2] = byte(bits >> 16)
		out[i*4+3] = byte(bits >> 24)
	}
	return out
}

func TestWindowLagFindsShift(t *testing.T) {
	ref := patternPCM(calSampleRate, 60, 0)
	// the dub runs 0.4 s late: dub position p holds the content the
	// reference has at p-0.4s
	dub := patternPCM(calSampleRate, 60, 0.4)
	lag, cos := windowLag(ref, dub, 10, 30, 4)
	if math.Abs(lag-(-0.4)) > 0.02 {
		t.Errorf("late dub: lag %+.3f, want -0.4", lag)
	}
	if cos < 0.5 {
		t.Errorf("same-content correlation must be strong, got %.3f", cos)
	}

	// an early dub leads: content at p is the reference's p+0.4
	early := patternPCM(calSampleRate, 60, -0.4)
	lag, _ = windowLag(ref, early, 10, 30, 4)
	if math.Abs(lag-0.4) > 0.02 {
		t.Errorf("early dub: lag %+.3f, want +0.4", lag)
	}

	// unrelated audio must not settle on a strong peak
	rng := rand.New(rand.NewSource(5))
	noise := make([]float32, calSampleRate*60)
	for i := range noise {
		noise[i] = float32(rng.NormFloat64()) * 0.2
	}
	lag, cos = windowLag(ref, noise, 10, 30, 4)
	if math.IsNaN(lag) || math.Abs(lag) > 4.01 {
		t.Errorf("noise lag out of range: %v", lag)
	}
	if cos > 0.2 {
		t.Errorf("noise must correlate weakly, got %.3f", cos)
	}
}

func TestSegmentLagsAndApplyShifts(t *testing.T) {
	// reference PCM with the same burst pattern; the dub track sits 0.5 s
	// late in segment 1 and 0.7 s late in segment 2
	const span = 60.0
	ref := patternPCM(calSampleRate, span, 0)
	dub := make([]float32, len(ref))
	for i := range dub {
		t := float64(i) / calSampleRate
		late := 0.5
		if t >= span/2 {
			late = 0.7
		}
		dub[i] = float32(latticeNoise(t - late))
	}
	m := &timingMap{Speed: 1, Segments: []segment{
		{DubStart: 0, DubEnd: 30, RefStart: 0, RefEnd: 30, Slope: 1, Intercept: 0},
		{DubStart: 30, DubEnd: span, RefStart: 30, RefEnd: span, Slope: 1, Intercept: 0},
	}}
	shifts, notes, evidence := segmentLags(m, ref, dub)
	if evidence != 2 {
		t.Fatalf("evidence: got %d, want 2 (notes %v)", evidence, notes)
	}
	if math.Abs(shifts[0]-(-0.5)) > 0.05 || math.Abs(shifts[1]-(-0.7)) > 0.05 {
		t.Errorf("shifts: got %+.3f/%+.3f, want -0.5/-0.7", shifts[0], shifts[1])
	}
	applyShifts(m, shifts, span, span)
	if math.Abs(m.Segments[0].Intercept-(-0.5)) > 0.05 || math.Abs(m.Segments[1].Intercept-(-0.7)) > 0.05 {
		t.Errorf("intercepts after apply: %.4f/%.4f", m.Segments[0].Intercept, m.Segments[1].Intercept)
	}
	// the first span would start before the file and clamps to 0
	if m.Segments[0].RefStart != 0 {
		t.Errorf("ref start must clamp at the file head, got %.3f", m.Segments[0].RefStart)
	}
	if math.Abs(m.Segments[1].RefStart-29.3) > 0.05 || math.Abs(m.Segments[1].RefEnd-59.3) > 0.05 {
		t.Errorf("second span must move by its shift, got %.3f-%.3f", m.Segments[1].RefStart, m.Segments[1].RefEnd)
	}
}

func TestCalibrateAudioIntegration(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mkv")
	dub := filepath.Join(dir, "dub.ts")
	writeFile(t, ref, 1000)
	writeFile(t, dub, 1000)

	longRefJSON := `{"streams":[` +
		`{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
		`{"index":1,"codec_name":"ac3","codec_type":"audio"}],"format":{"duration":"60.0","size":"1000","bit_rate":"1"}}`
	longDubJSON := `{"streams":[` +
		`{"index":0,"codec_name":"mpeg2video","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
		`{"index":1,"codec_name":"mp2","codec_type":"audio"}],"format":{"duration":"58.0","size":"1000","bit_rate":"1"}}`
	ff := &fakeFF{
		probeFor: func(src string) (string, bool) {
			if filepath.Base(src) == "ref.mkv" {
				return longRefJSON, true
			}
			return longDubJSON, true
		},
		framesFor: func(src string) []byte {
			if filepath.Base(src) == "ref.mkv" {
				return frameSeeds(0, 1200) // 60 s at interval 0.05
			}
			return frameSeeds(10, 1160) // the dub starts 2 s in
		},
		rawFor: func(src string) []byte {
			if strings.HasSuffix(src, "dub.raw") {
				// the dub audio carries the same content 0.33 s later than
				// its video implies: with the +0.5 s map the dub track
				// measures 0.33 s late against the reference audio
				return s16leStereo(patternPCM(sampleRate, 58, -0.83))
			}
			return []byte("muxed output")
		},
		pcmFor: func(src string) []byte {
			return f32le(patternPCM(calSampleRate, 60, 0))
		},
	}

	out := &bytes.Buffer{}
	cfg := &Config{Ref: ref, Dub: dub, Out: filepath.Join(dir, "out.mkv"), Interval: 0.05, MatchDist: 45, Bitrate: "192k", LangRef: "eng", LangDub: "ces"}
	tl := newTestTool(t, dir, ff, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runOne(); err != nil {
		t.Fatalf("runOne: %v (out: %s)", err, out.String())
	}
	got := out.String()
	// the dub audio carries content 0.33 s later than its video implies,
	// and the +0.5 s frame map leaves the track 0.33 s early: the
	// calibration must measure and shift exactly that
	if !strings.Contains(got, "audio calibration: segment 1 +0.33s") {
		t.Errorf("missing calibration line: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.mkv")); err != nil {
		t.Errorf("output missing: %v", err)
	}
}

// s16leStereo interleaves a mono pattern into stereo s16le at the
// resampler's rate (both channels carry the signal, like real audio).
func s16leStereo(pcm []float32) []byte {
	out := make([]byte, len(pcm)*4)
	for i, v := range pcm {
		s := int16(v * 30000)
		binaryPut16(out[i*4:], uint16(s))
		binaryPut16(out[i*4+2:], uint16(s))
	}
	return out
}

func binaryPut16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func TestDebugSegmentLags(t *testing.T) {
	const span = 60.0
	ref := patternPCM(calSampleRate, span, 0)
	// the dub track plays the content 0.33 s early (as the integration
	// fixture's map+capture combination produces)
	dub := patternPCM(calSampleRate, span, -0.33)
	m := &timingMap{Speed: 1, Segments: []segment{
		{DubStart: 0, DubEnd: 58, RefStart: 1, RefEnd: 59, Slope: 1, Intercept: 0.5},
	}}
	lags := []float64{}
	win := math.Min(calWindow, math.Max(12, (59-1-2)/3))
	for c := 1.0 + calMargin; c <= 59-calMargin-win; c += calHop {
		lag, cos := windowLag(ref, dub, c, win, 4)
		lags = append(lags, lag)
		fmt.Printf("c=%.0f lag %+.3f cos %.3f\n", c, lag, cos)
	}
	_ = lags
	shifts, notes, evidence := segmentLags(m, ref, dub)
	fmt.Printf("shifts %v notes %v evidence %d\n", shifts, notes, evidence)
}
