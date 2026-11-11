package align

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const sampleRate = 48000

// dubFrames streams raw s16le stereo frames with forward-only access: the
// timing map is monotonic, so resampling only ever advances through the
// dub (Python's whole-file numpy load, minus the gigabytes of RAM).
type dubFrames struct {
	br   *bufio.Reader
	pos  int64    // index of the next frame to read
	last [2]int16 // frame pos-1, kept for interpolating exactly there
	init bool     // whether last holds a real frame
}

// frame returns the dub frame at index i; ok is false past the dub's end
// (those ranges stay silent, like the numpy zero-padding).
func (d *dubFrames) frame(i int64) ([2]int16, bool) {
	if i < d.pos-1 {
		// cannot happen: interp1 over the monotonic map only moves forward
		return [2]int16{}, false
	}
	if i == d.pos-1 && d.init {
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
	d.last, d.init = f, true
	d.pos++
	return f, true
}

// resampleDub streams the dubbed raw audio onto the reference timeline:
// output sample t reads the dub at t' = interp(t, enGrid, czGrid),
// linearly interpolating between the neighboring dub samples. Port of
// align_audio.py resample_audio.
func resampleDub(src io.Reader, dst io.Writer, czGrid, enGrid []float64, refDur float64) error {
	d := &dubFrames{br: bufio.NewReaderSize(src, 1<<20)}
	bw := bufio.NewWriterSize(dst, 1<<20)
	nOut := int(refDur * float64(sampleRate))
	const chunk = 1_000_000
	for start := 0; start < nOut; start += chunk {
		end := min(start+chunk, nOut)
		for t := start; t < end; t++ {
			tCz := interp1(enGrid, czGrid, float64(t)/float64(sampleRate))
			idx := tCz * float64(sampleRate)
			i0 := int64(math.Floor(idx))
			frac := float32(idx - float64(i0))
			s0, _ := d.frame(i0)
			s1, _ := d.frame(i0 + 1)
			var out [4]byte
			for ch := 0; ch < 2; ch++ {
				v := float32(s0[ch])*(1-frac) + float32(s1[ch])*frac
				binary.LittleEndian.PutUint16(out[ch*2:], uint16(clamp16(v)))
			}
			if _, err := bw.Write(out[:]); err != nil {
				return fmt.Errorf("cannot write resampled audio: %w", err)
			}
		}
	}
	return bw.Flush()
}

func clamp16(v float32) int {
	return min(max(int(math.Round(float64(v))), -32768), 32767)
}
