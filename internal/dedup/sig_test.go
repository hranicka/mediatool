package dedup

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

// patternFrame fills one 32x32 gray frame with deterministic pseudo-random
// content seeded by the frame index: frame t is always the same pixels, so
// the same content produces the same hash sequence (possibly shifted),
// while different frames and different files hash far apart. (A plain
// gradient would not do: the median-threshold hash is brightness
// invariant, so every gradient frame would hash identically.)
func patternFrame(t int) []byte {
	rng := rand.New(rand.NewSource(int64(t)))
	frame := make([]byte, frameLen)
	rng.Read(frame)
	return frame
}

// noiseFrames produces per-frame pseudo-random gray data, i.e. content no
// other file shares.
func noiseFrames(seed, count int) []byte {
	rng := rand.New(rand.NewSource(int64(seed)))
	buf := make([]byte, count*frameLen)
	rng.Read(buf)
	return buf
}

// framesAt builds the raw stream of count frames starting at frame index
// start (frame k of the stream is pattern frame start+k).
func framesAt(start, count int) []byte {
	buf := make([]byte, 0, count*frameLen)
	for t := start; t < start+count; t++ {
		buf = append(buf, patternFrame(t)...)
	}
	return buf
}

func TestFrameHashStable(t *testing.T) {
	a := frameHash(patternFrame(10))
	b := frameHash(patternFrame(10))
	if hamming(a, b) != 0 {
		t.Fatal("hashing the same frame twice must be deterministic")
	}
}

func TestFrameHashToleratesSmallChanges(t *testing.T) {
	frame := patternFrame(5)
	perturbed := make([]byte, len(frame))
	copy(perturbed, frame)
	for i := 0; i < 50; i++ { // encoder noise: a few slightly different pixels
		perturbed[i*17%len(perturbed)]++
	}
	d := hamming(frameHash(frame), frameHash(perturbed))
	if d > hashLen*8/10 { // comfortably below the default 15% threshold
		t.Errorf("small pixel noise must barely move the hash: %d bits differ", d)
	}
}

func TestFrameHashDifferentContentFarApart(t *testing.T) {
	a := frameHash(patternFrame(3))
	b := frameHash(noiseFrames(1, 1)[:frameLen])
	if d := hamming(a, b); d < hashLen*8/4 {
		t.Errorf("unrelated frames must hash far apart, got %d differing bits", d)
	}
}

func TestMedian(t *testing.T) {
	if got := median([]float64{3, 1, 2}); got != 2 {
		t.Errorf("odd count: got %v, want 2", got)
	}
	if got := median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("even count: got %v, want 2.5", got)
	}
}

type fakeFF struct {
	probeFor  func(src string) (string, bool)
	framesFor func(src string) []byte
	frameArgs [][]string
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
			if args[i] == "-i" {
				f.frameArgs = append(f.frameArgs, args)
				if f.framesFor != nil {
					return f.framesFor(args[i+1]), nil
				}
			}
		}
		return []byte{}, nil
	}
	return []byte{}, nil
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	return nil, nil
}

func TestExtractSignatures(t *testing.T) {
	raw := framesAt(4, 7)
	ff := &fakeFF{framesFor: func(src string) []byte { return raw }}

	hashes, err := extractSignatures(ff, "ffmpeg", "/x/a.mkv", 2)
	if err != nil {
		t.Fatalf("extractSignatures: %v", err)
	}
	if len(hashes) != 7 {
		t.Fatalf("got %d signatures, want 7", len(hashes))
	}
	for i := range hashes {
		if hashes[i] != frameHash(raw[i*frameLen:(i+1)*frameLen]) {
			t.Errorf("signature %d does not match its frame", i)
		}
	}

	// the extraction must be a single ffmpeg pass with the sampling chain
	args := ff.frameArgs[0]
	for _, pair := range [][2]string{
		{"-vf", "fps=1/2,scale=32:32,format=gray"},
		{"-f", "rawvideo"},
	} {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == pair[0] && args[i+1] == pair[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("args missing %q %q: %v", pair[0], pair[1], args)
		}
	}
}

func TestExtractSignaturesNoFrames(t *testing.T) {
	ff := &fakeFF{framesFor: func(string) []byte { return nil }}
	hashes, err := extractSignatures(ff, "ffmpeg", "/x/a.mkv", 2)
	if err != nil {
		t.Fatalf("empty output must not be an error: %v", err)
	}
	if len(hashes) != 0 {
		t.Errorf("got %d signatures, want 0", len(hashes))
	}
}

func TestExtractSignaturesError(t *testing.T) {
	ff := &failingFF{}
	if _, err := extractSignatures(ff, "ffmpeg", "/x/a.mkv", 2); err == nil {
		t.Fatal("expected an error from a failing ffmpeg run")
	}
}

type failingFF struct{ fakeFF }

func (f *failingFF) Output(name string, args ...string) ([]byte, error) {
	return nil, errors.New("boom")
}

func TestClock(t *testing.T) {
	tests := map[float64]string{
		0:      "0:00:00",
		59.4:   "0:00:59",
		600:    "0:10:00",
		5412.6: "1:30:13",
	}
	for in, want := range tests {
		if got := clock(in); got != want {
			t.Errorf("clock(%v): got %s, want %s", in, got, want)
		}
	}
}

func videoJSON(codec string, w, h int, dur string) string {
	return fmt.Sprintf(`{"streams":[{"index":0,"codec_name":"%s","codec_type":"video","width":%d,"height":%d,"disposition":{"attached_pic":0}}],"format":{"duration":"%s","size":"1000","bit_rate":"1"}}`, codec, w, h, dur)
}

func TestDurationCompatible(t *testing.T) {
	tests := []struct {
		d1, d2, tol float64
		want        bool
	}{
		{600, 600, 5, true},
		{600, 609, 5, true},  // inside the 10 s floor
		{600, 620, 5, true},  // inside 5% of 600 (=30 s)
		{600, 650, 5, false}, // 50 s > 30 s
		{600, 400, 5, false},
		{3600, 3760, 5, true},  // long files: 5% is 180 s
		{3600, 3800, 5, false}, // 200 s > 180 s
	}
	for _, tt := range tests {
		if got := durationCompatible(tt.d1, tt.d2, tt.tol); got != tt.want {
			t.Errorf("durationCompatible(%v, %v, %v): got %v, want %v", tt.d1, tt.d2, tt.tol, got, tt.want)
		}
	}
}
