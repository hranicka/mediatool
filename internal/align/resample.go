package align

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const sampleRate = 48000

// fadeSeconds smooths the edges of the dub body so the transitions to and
// from silence do not click.
const fadeSeconds = 0.02

// rewindFrames is how many recent frames the reader keeps (a power of
// two). The timing line is strictly monotonic, so the mapped dub position
// never rewinds; the small ring only serves the interpolation overlap - a
// stretched map (speed < 1) interpolates around the same dub index for
// many consecutive output samples.
const rewindFrames = 16

// dubFrames streams raw s16le stereo frames with a small ring of recent
// frames.
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

// resampleDub streams the dubbed raw audio onto the reference timeline
// through the timing line: reference sample t reads dub time
// (t - intercept)/speed. Regions with no dub counterpart (an extra
// reference intro or credits tail) stay silent, dub regions before the
// reference start or past its end are dropped, and linear interpolation
// between neighboring dub samples keeps speed conversions seamless.
// audioLead is the leading gap dropped by the raw extraction: dubbed time
// minus the lead addresses the first extracted sample.
func resampleDub(src io.Reader, dst io.Writer, l line, refDur, dubDur, audioLead float64) error {
	d := &dubFrames{br: bufio.NewReaderSize(src, 1<<20)}
	bw := bufio.NewWriterSize(dst, 1<<20)
	nOut := int(refDur * float64(sampleRate))
	bodyStart := max(0.0, l.refAt(0))
	bodyEnd := refDur
	if e := l.refAt(dubDur); e < bodyEnd {
		bodyEnd = e
	}
	const chunk = 1_000_000
	var silence [4]byte
	for start := 0; start < nOut; start += chunk {
		end := min(start+chunk, nOut)
		for t := start; t < end; t++ {
			sec := float64(t) / float64(sampleRate)
			gain := bodyGain(sec, bodyStart, bodyEnd, refDur)
			if gain <= 0 {
				if _, err := bw.Write(silence[:]); err != nil {
					return fmt.Errorf("cannot write resampled audio: %w", err)
				}
				continue
			}
			idx := (l.dubAt(sec) - audioLead) * float64(sampleRate)
			i0 := int64(math.Floor(idx))
			frac := float32(idx - float64(i0))
			s0, _ := d.frame(i0)
			s1, _ := d.frame(i0 + 1)
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

// bodyGain fades the dub body in and out over fadeSeconds where it
// borders real silence: the head or tail of the reference. Outside the
// body the gain is zero.
func bodyGain(sec, bodyStart, bodyEnd, refDur float64) float32 {
	gain := float32(1)
	if bodyStart > 0 {
		if in := sec - bodyStart; in < fadeSeconds {
			gain = min(gain, float32(max(in, 0)/fadeSeconds))
		}
	}
	if bodyEnd < refDur {
		if out := bodyEnd - sec; out < fadeSeconds {
			gain = min(gain, float32(max(out, 0)/fadeSeconds))
		}
	}
	return max(gain, 0)
}

func clamp16(v float32) int {
	return min(max(int(math.Round(float64(v))), -32768), 32767)
}
