package vhash

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// patternFrame fills one Side x Side gray frame with deterministic
// pseudo-random content seeded by the frame index: frame t is always the
// same pixels, so the same content produces the same hash sequence
// (possibly shifted), while different frames and different files hash far
// apart. (A plain gradient would not do: the median-threshold hash is
// brightness invariant, so every gradient frame would hash identically.)
func patternFrame(t int) []byte {
	rng := rand.New(rand.NewSource(int64(t)))
	frame := make([]byte, FrameLen)
	rng.Read(frame)
	return frame
}

// framesAt builds the raw stream of count frames starting at frame index
// start (frame k of the stream is pattern frame start+k).
func framesAt(start, count int) []byte {
	buf := make([]byte, 0, count*FrameLen)
	for t := start; t < start+count; t++ {
		buf = append(buf, patternFrame(t)...)
	}
	return buf
}

func TestFrameHashStable(t *testing.T) {
	a := FrameHash(patternFrame(10))
	b := FrameHash(patternFrame(10))
	if Hamming(a, b) != 0 {
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
	d := Hamming(FrameHash(frame), FrameHash(perturbed))
	if d > HashLen*8/10 { // comfortably below the default 15% threshold
		t.Errorf("small pixel noise must barely move the hash: %d bits differ", d)
	}
}

func TestFrameHashDifferentContentFarApart(t *testing.T) {
	a := FrameHash(patternFrame(3))
	rng := rand.New(rand.NewSource(1))
	var b [HashLen]byte
	rng.Read(b[:])
	if d := Hamming(a, b); d < HashLen*8/4 {
		t.Errorf("unrelated frames must hash far apart, got %d differing bits", d)
	}
}

func TestBackgroundDistance(t *testing.T) {
	// identical frames: background 0 (self-similar extreme)
	same := make([][HashLen]byte, 50)
	if got := BackgroundDistance(same, 5, 200); got != 0 {
		t.Errorf("identical frames: got %d, want 0", got)
	}
	// pseudo-random signatures: background ~half the bits
	rng := rand.New(rand.NewSource(3))
	var noise [][HashLen]byte
	for i := 0; i < 50; i++ {
		var h [HashLen]byte
		rng.Read(h[:])
		noise = append(noise, h)
	}
	if got := BackgroundDistance(noise, 5, 200); got < HashLen*8/3 || got > HashLen*8*2/3 {
		t.Errorf("random signatures: got %d, want within a third around %d", got, HashLen*8/2)
	}
	// not enough frames: -1
	if got := BackgroundDistance(noise[:4], 5, 200); got != -1 {
		t.Errorf("too few frames: got %d, want -1", got)
	}
	// deterministic: two runs agree
	first := BackgroundDistance(noise, 5, 200)
	second := BackgroundDistance(noise, 5, 200)
	if first != second {
		t.Errorf("BackgroundDistance must be deterministic: %d vs %d", first, second)
	}
}

func TestWindowBestWantOutOfRange(t *testing.T) {
	// identical signatures: every candidate ties at distance 0, the tie
	// must go to the position closest to want - including when want sits
	// past the end of the window (prefer the largest k)
	a := make([][HashLen]byte, 3)
	b := make([][HashLen]byte, 10)
	if best, dist := WindowBest(a, b, 0, 5, 10, 10); best != 9 || dist != 0 {
		t.Errorf("got %d dist %d, want 9 dist 0", best, dist)
	}
	if best, _ := WindowBest(a, b, 0, 2, 6, 5); best != 5 {
		t.Errorf("got %d, want 5 (closest to want inside the window)", best)
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
	framesFor func(src string) []byte
	frameArgs [][]string
}

func (f *fakeFF) Output(name string, args ...string) ([]byte, error) {
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

func TestExtract(t *testing.T) {
	raw := framesAt(4, 7)
	ff := &fakeFF{framesFor: func(src string) []byte { return raw }}

	hashes, err := Extract(ff, "ffmpeg", "/x/a.mkv", 2)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(hashes) != 7 {
		t.Fatalf("got %d signatures, want 7", len(hashes))
	}
	for i := range hashes {
		if hashes[i] != FrameHash(raw[i*FrameLen:(i+1)*FrameLen]) {
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

func TestExtractNoFrames(t *testing.T) {
	ff := &fakeFF{framesFor: func(string) []byte { return nil }}
	hashes, err := Extract(ff, "ffmpeg", "/x/a.mkv", 2)
	if err != nil {
		t.Fatalf("empty output must not be an error: %v", err)
	}
	if len(hashes) != 0 {
		t.Errorf("got %d signatures, want 0", len(hashes))
	}
}

type failingFF struct{ fakeFF }

func (f *failingFF) Output(name string, args ...string) ([]byte, error) {
	return nil, errors.New("boom")
}

func TestExtractError(t *testing.T) {
	if _, err := Extract(&failingFF{}, "ffmpeg", "/x/a.mkv", 2); err == nil {
		t.Fatal("expected an error from a failing ffmpeg run")
	}
}

func TestExtractArgsSequence(t *testing.T) {
	// the -i path must be consumed as the input, not an output name
	ff := &fakeFF{framesFor: func(src string) []byte {
		if src != "/x/a.mkv" {
			return nil
		}
		return framesAt(0, 1)
	}}
	hashes, err := Extract(ff, "ffmpeg", "/x/a.mkv", 2)
	if err != nil || len(hashes) != 1 {
		t.Fatalf("Extract: %v, %d hashes", err, len(hashes))
	}
	if !strings.HasSuffix(ff.frameArgs[0][len(ff.frameArgs[0])-1], "pipe:1") {
		t.Errorf("frames must be read from stdout, got %v", ff.frameArgs[0])
	}
}
