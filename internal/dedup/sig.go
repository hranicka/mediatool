package dedup

import (
	"fmt"
	"math/bits"
	"sort"

	"github.com/hranicka/mediatool/internal/run"
)

// The frame signature: a 32x32 grayscale frame smoothed with a 3x3 box
// kernel and thresholded against its own median, packed row-major into
// hashLen bytes. Port of the align_audio.py phash: robust against encoder
// noise, brightness shifts and resolution changes, so re-encodes of the
// same scene hash nearly identically while different scenes stay far apart.
const (
	hashSide = 32
	hashLen  = hashSide * hashSide / 8 // 1024 bits = 128 bytes
	frameLen = hashSide * hashSide     // one raw grayscale frame from ffmpeg
)

// hamming returns the number of differing bits between two signatures.
func hamming(a, b [hashLen]byte) int {
	n := 0
	for i := range a {
		n += bits.OnesCount8(a[i] ^ b[i])
	}
	return n
}

// frameHash computes the signature of one raw 32x32 grayscale frame.
func frameHash(frame []byte) [hashLen]byte {
	const w, h = hashSide, hashSide
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
	var out [hashLen]byte
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

// extractSignatures runs one ffmpeg pass over path, sampling frames at
// every interval seconds down to 32x32 grayscale, and hashes each frame.
// Files with no sampled frames (no video, or shorter than the interval)
// return an empty signature list.
func extractSignatures(r run.Runner, ffmpegBin, path string, interval float64) ([][hashLen]byte, error) {
	vf := fmt.Sprintf("fps=1/%g,scale=%d:%d,format=gray", interval, hashSide, hashSide)
	raw, err := r.Output(ffmpegBin, "-v", "error", "-nostdin", "-i", path,
		"-vf", vf, "-f", "rawvideo", "pipe:1")
	if err != nil {
		return nil, fmt.Errorf("cannot extract frames: %w", err)
	}
	n := len(raw) / frameLen
	out := make([][hashLen]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, frameHash(raw[i*frameLen:(i+1)*frameLen]))
	}
	return out, nil
}
