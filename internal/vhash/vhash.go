// Package vhash computes perceptual frame signatures for video files: a
// frame is sampled every interval seconds (one ffmpeg rawvideo pass,
// downscaled grayscale), smoothed with a 3x3 box kernel and thresholded
// against its own median. The signatures are robust against encoder
// noise, brightness shifts and resolution changes, so re-encodes of the
// same scene hash nearly identically while different scenes stay far
// apart. Shared by dedup (content-duplicate search) and align (dub
// timing) — both port pieces of the align_audio.py scheme.
package vhash

import (
	"fmt"
	"math/bits"
	"sort"

	"github.com/hranicka/mediatool/internal/run"
)

const (
	// Side is the signature frame size (Side x Side grayscale pixels).
	Side = 32
	// HashLen is the signature size in bytes: Side*Side bits.
	HashLen = Side * Side / 8
	// FrameLen is one raw grayscale frame as delivered by ffmpeg.
	FrameLen = Side * Side
)

// Hamming returns the number of differing bits between two signatures.
func Hamming(a, b [HashLen]byte) int {
	n := 0
	for i := range a {
		n += bits.OnesCount8(a[i] ^ b[i])
	}
	return n
}

// FrameHash computes the signature of one raw Side x Side grayscale frame.
func FrameHash(frame []byte) [HashLen]byte {
	const w, h = Side, Side
	// 3x3 box smooth with edge padding
	sm := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := 0.0
			for dy := -1; dy <= 1; dy++ {
				yy := clampIdx(y+dy, h)
				for dx := -1; dx <= 1; dx++ {
					xx := clampIdx(x+dx, w)
					sum += float64(frame[yy*w+xx])
				}
			}
			sm[y*w+x] = sum / 9
		}
	}
	med := median(sm)
	var out [HashLen]byte
	for i, v := range sm {
		if v > med {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

func clampIdx(v, max int) int {
	if v < 0 {
		return 0
	}
	if v >= max {
		return max - 1
	}
	return v
}

// median returns the mean of the two middle values (np.median parity).
func median(v []float64) float64 {
	s := make([]float64, len(v))
	copy(s, v)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Extract runs one ffmpeg pass over path, sampling frames at every
// interval seconds down to Side x Side grayscale, and hashes each frame.
// Files with no sampled frames (no video, or shorter than the interval)
// return an empty signature list.
func Extract(r run.Runner, ffmpegBin, path string, interval float64) ([][HashLen]byte, error) {
	vf := fmt.Sprintf("fps=1/%g,scale=%d:%d,format=gray", interval, Side, Side)
	raw, err := r.Output(ffmpegBin, "-v", "error", "-nostdin", "-i", path,
		"-vf", vf, "-f", "rawvideo", "pipe:1")
	if err != nil {
		return nil, fmt.Errorf("cannot extract frames: %w", err)
	}
	n := len(raw) / FrameLen
	out := make([][HashLen]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, FrameHash(raw[i*FrameLen:(i+1)*FrameLen]))
	}
	return out, nil
}
