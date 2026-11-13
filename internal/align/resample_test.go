package align

import (
	"bufio"
	"bytes"
	"encoding/binary"
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

func sampleL(res []byte, frame int) int16 {
	return int16(binary.LittleEndian.Uint16(res[frame*4:]))
}

func oneSegment(dubEnd, refStart, refEnd, slope, intercept float64) *timingMap {
	return &timingMap{Segments: []segment{{
		DubStart: 0, DubEnd: dubEnd,
		RefStart: refStart, RefEnd: refEnd,
		Slope: slope, Intercept: intercept,
	}}}
}

func TestResampleDubConstantOffset(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(4 * sr)

	// +2 s offset: output sample t reads dub frame t-2 s; the reference is
	// 6 s long (2 s dub-free head + 4 s dub)
	tm := oneSegment(4, 2, 6, 1, 2)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 6.0, 0); err != nil {
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
	// before the dub covered region starts the track is silent
	for _, frame := range []int{0, sr, 2*sr - 1} {
		if got := sampleL(res, frame); got != 0 {
			t.Errorf("frame %d: got %d, want silence before the segment", frame, got)
		}
	}
}

func TestResampleDubSilentPastDubEnd(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(2 * sr) // 2 s dub

	tm := oneSegment(2, 0, 2, 1, 0)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 8.0, 0); err != nil {
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
			t.Errorf("frame %d must be silent past the covered span, got %d", frame, got)
		}
	}
}

func TestResampleDubGapsStaySilent(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(3 * sr)
	tm := &timingMap{Segments: []segment{
		{DubStart: 0, DubEnd: 1, RefStart: 0, RefEnd: 1, Slope: 1},
		{DubStart: 2, DubEnd: 3, RefStart: 2, RefEnd: 3, Slope: 1},
	}}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 3.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if got := sampleL(res, sr/2); got == 0 {
		t.Error("first segment must carry audio")
	}
	for frame := sr + 1; frame < 2*sr; frame += sr / 4 {
		if got := sampleL(res, frame); got != 0 {
			t.Fatalf("frame %d in the reference-only gap must be silent, got %d", frame, got)
		}
	}
	if got := sampleL(res, 2*sr+sr/2); got == 0 {
		t.Error("second segment must carry audio")
	}
}

func TestResampleDubFadesAtReferenceHead(t *testing.T) {
	const sr = sampleRate
	raw := make([]byte, sr/2*4)
	for i := 0; i < sr/2; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], 10000)
		binary.LittleEndian.PutUint16(raw[i*4+2:], 10000)
	}
	tm := oneSegment(0.5, 0.5, 1, 1, 0.5)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 1.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	fadeFrames := int(fadeSeconds * sr)
	if got := sampleL(res, sr/2); got != 0 {
		t.Errorf("segment head must start at silence, got %d", got)
	}
	if got := sampleL(res, sr/2+fadeFrames/2); got <= 0 || got >= 10000 {
		t.Errorf("mid-fade must be attenuated, got %d", got)
	}
	if got := sampleL(res, sr/2+2*fadeFrames); got != 10000 {
		t.Errorf("after the fade the gain must be full, got %d", got)
	}
}

func TestResampleDubNoDipAtSegmentJoin(t *testing.T) {
	const sr = sampleRate
	raw := make([]byte, sr*4)
	for i := 0; i < sr; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], 10000)
		binary.LittleEndian.PutUint16(raw[i*4+2:], 10000)
	}
	// two segments butted at 0.5 s: continuous content, no gap in between
	tm := &timingMap{Segments: []segment{
		{DubStart: 0, DubEnd: 0.5, RefStart: 0, RefEnd: 0.5, Slope: 1},
		{DubStart: 0.5, DubEnd: 1, RefStart: 0.5, RefEnd: 1, Slope: 1},
	}}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 1.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	for _, frame := range []int{sr/2 - 1, sr / 2, sr/2 + 1, sr - 2000} {
		if got := sampleL(res, frame); got != 10000 {
			t.Errorf("frame %d at the butted join: got %d, want 10000 (no fade)", frame, got)
		}
	}
}

func TestResampleDubRewindAtJoinNoSilence(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(3 * sr)
	// the second fitted line reads 0.1 s behind the first segment's end:
	// a fit artifact, not an edit. The reader must serve the recent frame
	// instead of writing silence until it catches up.
	tm := &timingMap{Segments: []segment{
		{DubStart: 0, DubEnd: 1, RefStart: 0, RefEnd: 1, Slope: 1},
		{DubStart: 0.9, DubEnd: 2, RefStart: 1, RefEnd: 2, Slope: 1, Intercept: 0.1},
	}}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 2.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	for frame := 0; frame < 2*sr; frame++ {
		if got := sampleL(res, frame); got == 0 {
			t.Fatalf("frame %d at the rewinding join is silent", frame)
		}
	}
}

func TestResampleDubStretchedMap(t *testing.T) {
	// ref = 2*dub: the dub plays at half speed, so consecutive output
	// samples interpolate around the same dub index. The two-frame cache
	// must keep the s0 term (a one-frame cache dropped it before).
	raw := make([]byte, 1000*4)
	for i := 0; i < 1000; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(2*i))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(2*i))
	}
	tm := oneSegment(0.02, 0, 0.04, 2, 0)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 0.04, 0); err != nil {
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

func TestResampleDubAudioLead(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(2 * sr)

	// the raw extraction dropped 0.5 s of leading silence: output 0.5 s
	// must address the first extracted frame, not the middle of the track
	tm := oneSegment(2, 0, 2, 1, 0)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 2.0, 0.5); err != nil {
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
	// the cache contract: i0 then i0+1 then i0 again (a stretched map
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
	tm := oneSegment(1, 0, 1, 1, 0)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 1.0, 0); err != nil {
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

func TestResampleDubSilenceMatchesMapGaps(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(10 * sr)
	// reference 0-12 s: 5 s of audio, a 2 s removed scene, then 5 s of
	// audio mapped from dub 5-10 s. Only the removed scene is silent.
	tm := &timingMap{Segments: []segment{
		{DubStart: 0, DubEnd: 5, RefStart: 0, RefEnd: 5, Slope: 1},
		{DubStart: 5, DubEnd: 10, RefStart: 7, RefEnd: 12, Slope: 1, Intercept: 2},
	}}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 12.0, 0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 12*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 12*sr*4)
	}
	silent := 0
	for frame := 0; frame < 12*sr; frame++ {
		if sampleL(res, frame) == 0 {
			silent++
		}
	}
	if silent < 2*sr || silent > 2*sr+2 {
		t.Errorf("silent samples: got %d (%.3f s), want ~%d (2 s)", silent, float64(silent)/sr, 2*sr)
	}
	for _, frame := range []int{5*sr - 1, 5 * sr, 7*sr - 1, 7*sr + int(fadeSeconds*sr), 12*sr - 1} {
		silentHere := sampleL(res, frame) == 0
		if want := frame >= 5*sr && frame < 7*sr; silentHere != want {
			t.Errorf("frame %d: silent=%v, want %v", frame, silentHere, want)
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
	tm := oneSegment(1, 0, 1, 1, -0.5/float64(sampleRate))
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, tm, 1.0, 0); err != nil {
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
