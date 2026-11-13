package align

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// dubRaw builds nDub frames of raw s16le stereo: left channel 1000+i%1000,
// right channel -(1000+i%1000).
func dubRaw(nDub int) []byte {
	raw := make([]byte, nDub*4)
	for i := 0; i < nDub; i++ {
		v := int16(1000 + i%1000)
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(-v))
	}
	return raw
}

// rampRaw builds nDub frames whose channels are i%mod: positions stay
// distinguishable across the whole track.
func rampRaw(nDub, mod int) []byte {
	raw := make([]byte, nDub*4)
	for i := 0; i < nDub; i++ {
		v := int16(i % mod)
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(v))
	}
	return raw
}

func sampleL(res []byte, frame int) int16 {
	return int16(binary.LittleEndian.Uint16(res[frame*4:]))
}

// oneSegmentMap builds a timing map with a single segment covering the
// whole reference: ref = intercept + speed*dub on [refStart, refEnd).
func oneSegmentMap(refStart, refEnd, slope, intercept float64) *timingMap {
	return &timingMap{Speed: slope, Segments: []segment{{
		RefStart: refStart, RefEnd: refEnd, Slope: slope, Intercept: intercept,
	}}}
}

func TestResampleDubConstantOffset(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(4 * sr)

	// +2 s offset: output sample t reads dub frame t-2 s; the reference is
	// 6 s long (2 s dub-free head + 4 s dub)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(2, 6, 1, 2), 6.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 6*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 6*sr*4)
	}
	for _, frame := range []int{2*sr + 2000, 3 * sr, 5*sr + 12345, 6*sr - 1} {
		dubIdx := frame - 2*sr
		if got, want := sampleL(res, frame), int16(1000+dubIdx%1000); got != want {
			t.Errorf("frame %d: got %d, want %d", frame, got, want)
		}
	}
	// before the dub body starts the track is silent
	for _, frame := range []int{0, sr, 2*sr - 1} {
		if got := sampleL(res, frame); got != 0 {
			t.Errorf("frame %d: got %d, want silence before the dub body", frame, got)
		}
	}
}

func TestResampleDubSilentPastDubEnd(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(2 * sr) // 2 s dub

	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0, 2, 1, 0), 8.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 8*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 8*sr*4)
	}
	if got := sampleL(res, sr); got != int16(1000+sr%1000) {
		t.Errorf("inside the dub: got %d", got)
	}
	for _, frame := range []int{2*sr + 5, 7 * sr} {
		if got := sampleL(res, frame); got != 0 {
			t.Errorf("frame %d must be silent past the dub end, got %d", frame, got)
		}
	}
}

func TestResampleDubDropsDubHead(t *testing.T) {
	const sr = sampleRate
	raw := rampRaw(6*sr, 30000) // 6 s dub

	// the dub runs 2 s ahead of the reference (a longer intro): the dub's
	// first 2 s have no reference counterpart and must be dropped
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0, 4, 1, -2), 4.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 4*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 4*sr*4)
	}
	for _, frame := range []int{0, 1, 12345, 4*sr - 1} {
		dubIdx := frame + 2*sr
		if got, want := sampleL(res, frame), int16(dubIdx%30000); got != want {
			t.Errorf("frame %d: got %d, want %d (dub frame %d)", frame, got, want, dubIdx)
		}
	}
}

func TestResampleDubAllSilentWhenNoOverlap(t *testing.T) {
	const sr = sampleRate
	// the line lands the whole dub outside the reference: degenerate, but
	// it must produce silence, not read frames or panic
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(dubRaw(4*sr)), &out, oneSegmentMap(100, 104, 1, 100), 2.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 2*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 2*sr*4)
	}
	for _, frame := range []int{0, sr, 2*sr - 1} {
		if got := sampleL(res, frame); got != 0 {
			t.Errorf("frame %d: got %d, want silence", frame, got)
		}
	}
}

func TestResampleDubFadesAtReferenceHead(t *testing.T) {
	const sr = sampleRate
	raw := make([]byte, sr/2*4)
	for i := 0; i < sr/2; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], 10000)
		binary.LittleEndian.PutUint16(raw[i*4+2:], 10000)
	}
	// the dub body starts 0.5 s into the reference
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0.5, 1, 1, 0.5), 1.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	fadeFrames := int(fadeSeconds * sr)
	if got := sampleL(res, sr/2); got != 0 {
		t.Errorf("body head must start at silence, got %d", got)
	}
	if got := sampleL(res, sr/2+fadeFrames/2); got <= 0 || got >= 10000 {
		t.Errorf("mid-fade must be attenuated, got %d", got)
	}
	if got := sampleL(res, sr/2+2*fadeFrames); got != 10000 {
		t.Errorf("after the fade the gain must be full, got %d", got)
	}
}

func TestResampleDubStretchedMap(t *testing.T) {
	// ref = 2*dub: the dub plays at half speed, so consecutive output
	// samples interpolate around the same dub index.
	raw := make([]byte, 1000*4)
	for i := 0; i < 1000; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(2*i))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(2*i))
	}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0, 0.04, 2, 0), 0.04, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	for _, frame := range []int{10, 11, 100, 101, 199, 200, 201} {
		idx := float64(frame) / 2
		i0 := int(idx)
		frac := idx - float64(i0)
		want := int(float64(2*i0)*(1-frac) + float64(2*(i0+1))*frac)
		if got := int(sampleL(res, frame)); got != want {
			t.Errorf("frame %d: got %d, want %d (interpolated)", frame, got, want)
		}
	}
}

func TestResampleDubPALStretch(t *testing.T) {
	const sr = sampleRate
	// a PAL-speedup dub (4.27 % faster) is stretched onto the reference:
	// output frame t reads dub position t/1.0427, interpolated
	raw := rampRaw(10000, 30000)
	var out bytes.Buffer
	refDur := 2.0 * 1.0427
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0, refDur, 1.0427, 0), refDur, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != int(refDur*sr)*4 {
		t.Fatalf("output size: got %d, want %d", len(res), int(refDur*sr)*4)
	}
	for _, frame := range []int{0, 1, 5000, 10008} {
		idx := float64(frame) / 1.0427
		i0 := int(math.Floor(idx))
		frac := idx - float64(i0)
		want := int(float64(i0%30000)*(1-frac) + float64((i0+1)%30000)*frac)
		if got := int(sampleL(res, frame)); math.Abs(float64(got-want)) > 1 {
			t.Errorf("frame %d: got %d, want ~%d (stretched)", frame, got, want)
		}
	}
}

func TestResampleDubAudioLead(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(2 * sr)

	// the raw extraction dropped 0.5 s of leading silence: output 0.5 s
	// must address the first extracted frame, not the middle of the track
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0, 2, 1, 0), 2.0, 0.5); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 2*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 2*sr*4)
	}
	for _, frame := range []int{0, sr/2 - 2, sr/2 - 1} {
		if got := sampleL(res, frame); got != 0 {
			t.Errorf("frame %d before the lead must be silent, got %d", frame, got)
		}
	}
	if got, want := sampleL(res, sr/2), int16(1000); got != want {
		t.Errorf("first sample after the lead: got %d, want %d", got, want)
	}
	if got, want := sampleL(res, sr/2+1234), int16(1000+1234%1000); got != want {
		t.Errorf("inside the lead-adjusted track: got %d, want %d", got, want)
	}
}

func TestDubFramesNegativeIndex(t *testing.T) {
	d := &dubFrames{br: bufio.NewReader(bytes.NewReader(dubRaw(10)))}
	if _, ok := d.frame(-1); ok {
		t.Fatal("a negative index must not return the first frame")
	}
	if f, ok := d.frame(0); !ok || f[0] != 1000 {
		t.Fatalf("frame 0 after the negative probe: got %v %v", f, ok)
	}
}

func TestDubFramesRepeatAccess(t *testing.T) {
	// the ring contract: i0 then i0+1 then i0 again (a stretched map
	// repeats i0) must serve the same frames, never silence
	d := &dubFrames{br: bufio.NewReader(bytes.NewReader(dubRaw(50)))}
	for _, i := range []int64{0, 1, 1, 2, 1, 3, 3, 2} {
		f, ok := d.frame(i)
		if !ok {
			t.Fatalf("frame %d: unexpected silence", i)
		}
		if want := int16(1000 + i%1000); f[0] != want {
			t.Errorf("frame %d: got %d, want %d", i, f[0], want)
		}
	}
}

func TestResampleDubTrailingPartialFrame(t *testing.T) {
	// raw audio not a multiple of 4 bytes: the trailing bytes become
	// silence, not an error
	full := dubRaw(100)
	raw := full[:len(full)-2] // half a frame
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, oneSegmentMap(0, 1, 1, 0), 1.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != sampleRate*4 {
		t.Fatalf("output size: got %d, want %d", len(res), sampleRate*4)
	}
	if got := sampleL(res, 98); got != int16(1000+98%1000) {
		t.Errorf("last complete frame: got %d", got)
	}
	for _, frame := range []int{99, 100, sampleRate - 1} {
		if got := sampleL(res, frame); got != 0 {
			t.Errorf("frame %d must be silent past the partial input, got %d", frame, got)
		}
	}
}

func TestResampleDubInterpolates(t *testing.T) {
	// a rising ramp: sample i holds value 2*i; a half-sample offset
	// must interpolate to the odd value between neighbors
	n := 1000
	raw := make([]byte, n*4)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(2*i))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(2*i))
	}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out,
		oneSegmentMap(0, 1, 1, -0.5/float64(sampleRate)), 1.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	// output frame t: idx = t + 0.5 → interpolates between samples t
	// (value 2t) and t+1 (value 2t+2) → 2t+1
	if got, want := sampleL(res, 10), int16(2*10+1); got != want {
		t.Errorf("half-sample interpolation: got %d, want %d", got, want)
	}
	if got, want := sampleL(res, 500), int16(2*500+1); got != want {
		t.Errorf("half-sample interpolation: got %d, want %d", got, want)
	}
}
