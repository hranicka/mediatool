package align

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const sampleRate = 48000

// fadeSeconds smooths the edges of a timing segment so a cut between a
// dubbed scene and a reference-only gap does not click.
const fadeSeconds = 0.02

// dubFrames streams raw s16le stereo frames with forward-only access: the
// timing map is monotonic, so resampling only ever advances through the
// dub (Python's whole-file numpy load, minus the gigabytes of RAM). The
// two-frame cache serves map stretches (local slope < 1, e.g. a PAL-speedup
// dub aligned onto a slower reference), where consecutive output samples
// interpolate around the same dub index.
type dubFrames struct {
	br   *bufio.Reader
	pos  int64    // index of the next frame to read
	prev [2]int16 // frame pos-2
	last [2]int16 // frame pos-1
	init int      // how many of prev/last are valid: 0, 1 (last) or 2
}

// frame returns the dub frame at index i; ok is false past the dub's end
// (those ranges stay silent).
func (d *dubFrames) frame(i int64) ([2]int16, bool) {
	if i < d.pos-2 {
		// cannot happen: segments advance monotonically
		return [2]int16{}, false
	}
	if i == d.pos-2 && d.init >= 2 {
		return d.prev, true
	}
	if i == d.pos-1 && d.init >= 1 {
		return d.last, true
	}
	for skip := i - d.pos; skip > 0; skip-- {
		if _, ok := d.read(); !ok {
			return [2]int16{}, false
		}
	}
	return d.read()
}

func (d *dubFrames) read() ([2]int16, bool) {
	var b [4]byte
	if _, err := io.ReadFull(d.br, b[:]); err != nil {
		return [2]int16{}, false
	}
	f := [2]int16{
		int16(binary.LittleEndian.Uint16(b[0:2])),
		int16(binary.LittleEndian.Uint16(b[2:4])),
	}
	d.prev, d.last = d.last, f
	if d.init < 2 {
		d.init++
	}
	d.pos++
	return f, true
}

// resampleDub streams the dubbed raw audio onto the reference timeline:
// each segment maps its reference span back to dubbed time; reference
// regions not covered by any segment stay silent. Linear interpolation
// between neighboring dub samples keeps framerate variants seamless.
func resampleDub(src io.Reader, dst io.Writer, tm *timingMap, refDur float64) error {
	d := &dubFrames{br: bufio.NewReaderSize(src, 1<<20)}
	bw := bufio.NewWriterSize(dst, 1<<20)
	nOut := int(refDur * float64(sampleRate))
	const chunk = 1_000_000
	segIdx := 0
	segs := tm.Segments
	for start := 0; start < nOut; start += chunk {
		end := min(start+chunk, nOut)
		for t := start; t < end; t++ {
			sec := float64(t) / float64(sampleRate)
			for segIdx < len(segs) && sec >= segs[segIdx].RefEnd {
				segIdx++
			}
			if segIdx >= len(segs) || sec < segs[segIdx].RefStart {
				var silence [4]byte
				if _, err := bw.Write(silence[:]); err != nil {
					return fmt.Errorf("cannot write resampled audio: %w", err)
				}
				continue
			}
			seg := segs[segIdx]
			idx := seg.dubAt(sec) * float64(sampleRate)
			i0 := int64(math.Floor(idx))
			frac := float32(idx - float64(i0))
			s0, _ := d.frame(i0)
			s1, _ := d.frame(i0 + 1)
			gain := segmentGain(seg, sec, refDur)
			var out [4]byte
			for ch := 0; ch < 2; ch++ {
				v := (float32(s0[ch])*(1-frac) + float32(s1[ch])*frac) * gain
				binary.LittleEndian.PutUint16(out[ch*2:], uint16(clamp16(v)))
			}
			if _, err := bw.Write(out[:]); err != nil {
				return fmt.Errorf("cannot write resampled audio: %w", err)
			}
		}
	}
	return bw.Flush()
}

// segmentGain fades a segment in and out over fadeSeconds when its edge
// borders a reference-only gap; edges at the file boundaries need no fade.
func segmentGain(s segment, sec, refDur float64) float32 {
	gain := float32(1)
	if s.RefStart > 0 {
		if in := sec - s.RefStart; in < fadeSeconds {
			gain = min(gain, float32(in/fadeSeconds))
		}
	}
	if s.RefEnd < refDur {
		if out := s.RefEnd - sec; out < fadeSeconds {
			gain = min(gain, float32(out/fadeSeconds))
		}
	}
	return max(gain, 0)
}

func clamp16(v float32) int {
	return min(max(int(math.Round(float64(v))), -32768), 32767)
}
