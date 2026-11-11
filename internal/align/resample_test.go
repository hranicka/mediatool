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

func linearGrids(offset float64, n int) (cz, en []float64) {
	cz = make([]float64, n+1)
	en = make([]float64, n+1)
	for i := 0; i <= n; i++ {
		cz[i] = float64(i)
		en[i] = float64(i) + offset
	}
	return cz, en
}

func sampleL(res []byte, frame int) int16 {
	return int16(binary.LittleEndian.Uint16(res[frame*4:]))
}

func TestResampleDubConstantOffset(t *testing.T) {
	const sr = sampleRate
	const dubSecs = 4
	raw := dubRaw(dubSecs * sr)

	// +2 s offset: output sample t reads dub frame t-2 s; the reference is
	// 6 s long (2 s silence-shifted head + 4 s dub)
	cz, en := linearGrids(2, 6)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, cz, en, 6.0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	if len(res) != 6*sr*4 {
		t.Fatalf("output size: got %d, want %d", len(res), 6*sr*4)
	}
	for _, frame := range []int{2*sr + 10, 3 * sr, 5*sr + 12345, 6*sr - 1} {
		dubIdx := frame - 2*sr
		if got, want := sampleL(res, frame), int16(1000+dubIdx%1000); got != want {
			t.Errorf("frame %d: got %d, want %d", frame, got, want)
		}
	}
	// before the dub starts the map clamps to dub frame 0 (np.interp parity)
	if got, want := sampleL(res, 0), int16(1000); got != want {
		t.Errorf("frame 0: got %d, want %d (clamped dub frame 0)", got, want)
	}
}

func TestResampleDubSilentPastDubEnd(t *testing.T) {
	const sr = sampleRate
	raw := dubRaw(2 * sr) // 2 s dub

	// no offset, but an 8 s reference: everything past the dub stays silent
	cz, en := linearGrids(0, 8)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, cz, en, 8.0); err != nil {
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
			t.Errorf("frame %d must be silent past the dub's end, got %d", frame, got)
		}
	}
}

func TestResampleDubStretchedMap(t *testing.T) {
	// slope-0.5 map (a PAL-speedup dub aligned onto a slower reference):
	// consecutive output samples interpolate around the same dub index, so
	// the s0 term must survive the repeat - the one-frame cache used to
	// silently drop it for about half the samples (P0 from review)
	raw := make([]byte, 200*4)
	for i := 0; i < 200; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(2*i))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(2*i))
	}
	// grids in "seconds" scaled so 1 s = 100 dub frames: the dub is played
	// at half speed onto a doubled reference timeline
	cz := []float64{0, 0.01, 0.02}
	en := []float64{0, 0.02, 0.04}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, cz, en, 0.04); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	// output frame t (dub-frame units) reads the dub at t/2, interpolating
	// the ramp: integer positions hold 2*idx, halfway positions hold 2t-1
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
	cz, en := linearGrids(0, 1)
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, cz, en, 1.0); err != nil {
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
	// a rising ramp: sample i holds value 2*i, so a half-sample offset
	// must interpolate to the odd value between neighbors
	n := 1000
	raw := make([]byte, n*4)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(raw[i*4:], uint16(2*i))
		binary.LittleEndian.PutUint16(raw[i*4+2:], uint16(2*i))
	}
	// shift by 0.5 samples: build grids in frames of 1 s at 48 kHz is too
	// coarse — use a tiny sr-independent check via a 1-frame offset grid:
	// cz/en over "seconds" scaled so 1 s = 2 samples (grids drive interp).
	cz := []float64{0, 1, 2, 3}
	en := []float64{0, 1, 2, 3}
	// emulate 0.5-sample shift: resample with refDur such that output
	// t reads input at (t/48000) but grid maps en->cz shifted by half a
	// sample: cz[i] = en[i] - (0.5/48000)
	for i := range cz {
		cz[i] = en[i] - 0.5/float64(sampleRate)
	}
	var out bytes.Buffer
	if err := resampleDub(bytes.NewReader(raw), &out, cz, en, 1.0); err != nil {
		t.Fatalf("resampleDub: %v", err)
	}
	res := out.Bytes()
	// output frame t: idx = cz(t/48000)*48000 = t - 0.5 → interpolates
	// between samples t-1 (value 2(t-1)) and t (value 2t) → 2t-1
	if got, want := sampleL(res, 10), int16(2*10-1); got != want {
		t.Errorf("half-sample interpolation: got %d, want %d", got, want)
	}
	if got, want := sampleL(res, 500), int16(2*500-1); got != want {
		t.Errorf("half-sample interpolation: got %d, want %d", got, want)
	}
}
