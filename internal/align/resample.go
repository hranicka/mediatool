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

// rewindFrames is how far the frame reader can step back (a power of two,
// about 1.4 s at 48 kHz). The map is normalized to be monotonic, but a
// tiny fit artifact at a segment join must not turn into silence.
const rewindFrames = 1 << 16

// dubFrames streams raw s16le stereo frames with a short rewind buffer.
// The map advances monotonically, so a window of recent frames is enough
// to serve stretches where consecutive output samples interpolate around
// the same dub index (local slope < 1, e.g. a PAL-speedup dub aligned onto
// a slower reference) and to absorb small fit artifacts at joins.
type dubFrames struct {
	br  *bufio.Reader
	pos int64      // index of the next frame to read
	buf [][2]int16 // ring of the last rewindFrames frames
}

// frame returns the dub frame at index i; ok is false before the dub's
// first sample, beyond the rewind window or past its end (those ranges
// stay silent).
func (d *dubFrames) frame(i int64) ([2]int16, bool) {
	if i < 0 {
		return [2]int16{}, false
	}
	if d.buf == nil {
		d.buf = make([][2]int16, rewindFrames)
	}
	if i < d.pos {
		if i < d.pos-rewindFrames {
			return [2]int16{}, false
		}
		return d.buf[i&(rewindFrames-1)], true
	}
	for d.pos < i {
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
	d.buf[d.pos&(rewindFrames-1)] = f
	d.pos++
	return f, true
}

// resampleDub streams the dubbed raw audio onto the reference timeline:
// each segment maps its reference span back to dubbed time; reference
// regions not covered by any segment stay silent. Linear interpolation
// between neighboring dub samples keeps framerate variants seamless.
// audioLead is the leading gap dropped by the raw extraction: dubbed time
// minus the lead addresses the first extracted sample.
func resampleDub(src io.Reader, dst io.Writer, tm *timingMap, refDur, audioLead float64) error {
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
			idx := (seg.dubAt(sec) - audioLead) * float64(sampleRate)
			i0 := int64(math.Floor(idx))
			frac := float32(idx - float64(i0))
			s0, _ := d.frame(i0)
			s1, _ := d.frame(i0 + 1)
			gain := segmentGain(segs, segIdx, sec, refDur)
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

// segmentGain fades a segment in and out over fadeSeconds where it borders
// real silence: the head/tail of the file or a reference-only gap. Edges
// butted against another segment stay at full gain, so continuous music
// does not dip at a map join.
func segmentGain(segs []segment, idx int, sec, refDur float64) float32 {
	s := segs[idx]
	gapBefore := s.RefStart > 0 && (idx == 0 || s.RefStart-segs[idx-1].RefEnd > 1e-3)
	gapAfter := s.RefEnd < refDur
	if idx+1 < len(segs) && segs[idx+1].RefStart-s.RefEnd <= 1e-3 {
		gapAfter = false
	}
	gain := float32(1)
	if gapBefore {
		if in := sec - s.RefStart; in < fadeSeconds {
			gain = min(gain, float32(in/fadeSeconds))
		}
	}
	if gapAfter {
		if out := s.RefEnd - sec; out < fadeSeconds {
			gain = min(gain, float32(out/fadeSeconds))
		}
	}
	return max(gain, 0)
}

func clamp16(v float32) int {
	return min(max(int(math.Round(float64(v))), -32768), 32767)
}
