package align

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"

	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/ui"
)

// Audio-based calibration of the timing map. The frame matches align the
// dub's VIDEO to the reference video, but the dub file's audio stream can
// sit at its own offset relative to the dub video - broadcast captures
// routinely carry a few hundred milliseconds - and can drift against the
// video clock. That offset is invisible to frame matching and shifts the
// whole dub track out of lipsync. After a first resample, the shared
// music/effects bed between the language versions is cross-correlated
// window by window on the reference timeline (raw waveform, so beat
// ambiguity cannot bias the peak); the per-segment median lag shifts the
// map's intercepts, and the resample is repeated until the measured lag
// vanishes.

const (
	calSampleRate = 8000 // measurement PCM rate
	calWindow     = 30.0 // correlation window, seconds
	calHop        = 5.0  // window hop, seconds
	calLag        = 4.0  // lag search radius, seconds
	calMargin     = 1.0  // keep windows this far from segment edges
	calMinWindows = 3    // windows per segment before its median counts
	calAgreement  = 0.4  // at least this share of windows must sit within
	// calAgreeBand of the median lag: the true lag curve is smooth, so
	// scattered windows are correlation garbage, not evidence
	calAgreeBand = 0.15
	calMaxShift  = 2.0  // refuse to move a segment by more than this
	calDone      = 0.06 // residual lag below this needs no further round
	calRounds    = 3    // measure-shift rounds at most
)

// calibrateAudio measures the per-segment audio lag of the resampled dub
// (resRawPath, s16le stereo as written by resampleDub) against the
// reference audio and shifts the map's intercepts by it, repeating the
// resample until the measured lag vanishes. It reports every round.
func (t *tool) calibrateAudio(in *inputs, m *timingMap, dubRaw, resRawPath string) error {
	refPCM, err := t.extractCalibrationPCM(in.ref, in.refInfo)
	if err != nil {
		return err
	}
	var lastReport string
	for round := 0; round < calRounds; round++ {
		dubPCM, err := stereoToCalPCM(resRawPath)
		if err != nil {
			return err
		}
		shifts, notes, evidence := segmentLags(m, refPCM, dubPCM)
		applyShifts(m, shifts, in.refDur, in.dubDur)
		if evidence == 0 {
			for _, n := range notes {
				t.rep.Note("note: audio calibration: " + n)
			}
			return fmt.Errorf("no window correlated strongly enough (check the pairing and the reference audio)")
		}
		line := "audio calibration:"
		applied := 0
		for i := range m.Segments {
			line += fmt.Sprintf(" segment %d %+.2fs,", i+1, shifts[i])
			if shifts[i] != 0 {
				applied++
			}
		}
		line = line[:len(line)-1]
		if round == 0 {
			t.rep.Raw(ui.Neutral, "%s\n", line)
			for _, n := range notes {
				t.rep.Note("note: audio calibration: " + n)
			}
		} else {
			lastReport = line
		}
		if applied == 0 {
			return nil // converged: nothing left to shift
		}
		if err := t.resampleAgain(dubRaw, resRawPath, m, in); err != nil {
			return err
		}
	}
	if lastReport != "" {
		t.rep.Note("note: audio calibration settled at: " + lastReport)
	}
	return nil
}

// segmentLags measures the audio lag per segment and returns the
// intercept shifts, notes for segments without evidence, and the number
// of segments with evidence. A positive shift means the dub track was
// measured EARLY, so the intercept grows; a negative one means late.
func segmentLags(m *timingMap, refPCM, dubPCM []float32) (shifts []float64, notes []string, evidence int) {
	shifts = make([]float64, len(m.Segments))
	for i, s := range m.Segments {
		span := s.RefEnd - s.RefStart - 2*calMargin
		if span < 8 {
			notes = append(notes, fmt.Sprintf("segment %d: too short to measure (%.1fs), left as fitted", i+1, s.RefEnd-s.RefStart))
			continue
		}
		// short segments take a shorter window so at least a few fit
		win := math.Min(calWindow, math.Max(12, span/3))
		lagRadius := math.Min(calLag, span/2-1)
		// windows fully inside the segment, away from its silent edges
		var lags []float64
		for c := s.RefStart + calMargin; c <= s.RefEnd-calMargin-win; c += calHop {
			if lag, _ := windowLag(refPCM, dubPCM, c, win, lagRadius); !math.IsNaN(lag) {
				lags = append(lags, lag)
			}
		}
		if len(lags) < calMinWindows {
			notes = append(notes, fmt.Sprintf("segment %d: not enough audio evidence (%d windows), left as fitted", i+1, len(lags)))
			continue
		}
		shift := medianFloat(lags)
		agree := 0
		for _, l := range lags {
			if math.Abs(l-shift) <= calAgreeBand {
				agree++
			}
		}
		if float64(agree)/float64(len(lags)) < calAgreement {
			notes = append(notes, fmt.Sprintf("segment %d: the audio lag does not settle (%.0f%% of windows agree), left as fitted", i+1, float64(agree)/float64(len(lags))*100))
			continue
		}
		evidence++
		if math.Abs(shift) > calMaxShift {
			notes = append(notes, fmt.Sprintf("segment %d: measured audio lag %.2fs is implausible, left as fitted", i+1, shift))
			continue
		}
		if math.Abs(shift) < calDone {
			continue // already in sync within measurement noise
		}
		shifts[i] = shift
	}
	// a segment without stable evidence inherits the previous segment's
	// shift: the audio offset drifts slowly, so the neighbour's value is
	// accurate to well within perception
	for i := 1; i < len(shifts); i++ {
		if shifts[i] == 0 && shifts[i-1] != 0 {
			shifts[i] = shifts[i-1]
			notes = append(notes, fmt.Sprintf("segment %d: weak audio evidence, inherited segment %d's shift", i+1, i))
		}
	}
	return shifts, notes, evidence
}

// applyShifts moves the map's intercepts by the measured shifts: the dub
// track measured late (negative lag) plays earlier. Reference spans move
// with the intercepts and are re-clamped to the file.
func applyShifts(m *timingMap, shifts []float64, refDur, dubDur float64) {
	for i := range m.Segments {
		if shifts[i] == 0 {
			continue
		}
		s := &m.Segments[i]
		s.Intercept += shifts[i]
		s.RefStart = math.Min(math.Max(s.RefStart+shifts[i], 0), refDur)
		s.RefEnd = math.Min(math.Max(s.RefEnd+shifts[i], 0), refDur)
		s.DubStart = math.Max(0, s.dubAt(s.RefStart))
		s.DubEnd = math.Min(dubDur, s.dubAt(s.RefEnd))
		if s.DubEnd < s.DubStart {
			s.DubEnd = s.DubStart
		}
	}
}

// windowLag cross-correlates one reference window against the dub track
// on the raw PCM and returns the lag (dub-early positive, seconds) and
// the normalized correlation of the peak. The peak is refined
// parabolically. Zero-mean windows; circular FFT correlation.
func windowLag(refPCM, dubPCM []float32, start, win, lagRadius float64) (float64, float64) {
	c := int(start * calSampleRate)
	w := int(win * calSampleRate)
	lagN := int(lagRadius * calSampleRate)
	if c+w > len(refPCM) || c+w > len(dubPCM)-lagN || c-lagN < 0 {
		return math.NaN(), -1
	}
	size := 1
	for size < w+2*lagN {
		size <<= 1
	}
	a := make([]complex128, size)
	b := make([]complex128, size)
	var aMean, bMean float64
	for i := 0; i < w; i++ {
		aMean += float64(refPCM[c+i])
		bMean += float64(dubPCM[c+i])
	}
	aMean /= float64(w)
	bMean /= float64(w)
	for i := 0; i < w; i++ {
		a[i] = complex(float64(refPCM[c+i])-aMean, 0)
		b[i] = complex(float64(dubPCM[c+i])-bMean, 0)
	}
	fft(a)
	fft(b)
	for i := 0; i < size; i++ {
		a[i] = a[i] * conj(b[i])
	}
	inverseFFT(a)
	// corr[k] = sum a[n+k]*b[n]: the peak at k means the test content sits
	// k samples early on the reference timeline
	bestK, bestVal := 0, 0.0
	for k := -lagN; k <= lagN; k++ {
		v := corrAt(a, k, size)
		if math.Abs(v) > math.Abs(bestVal) {
			bestVal, bestK = v, k
		}
	}
	// normalized correlation at the peak: the windows were mean-subtracted
	// before the transform, so the peak value is the raw dot product
	sqA, sqB := 0.0, 0.0
	for i := 0; i < w; i++ {
		av := float64(refPCM[c+i]) - aMean
		bv := float64(dubPCM[c+i]) - bMean
		sqA += av * av
		sqB += bv * bv
	}
	cos := bestVal / math.Sqrt(sqA*sqB+1e-12)
	refine := 0.0
	{
		y0 := math.Abs(corrAt(a, bestK-1, size))
		y2 := math.Abs(corrAt(a, bestK+1, size))
		den := y0 - 2*math.Abs(bestVal) + y2
		if den < -1e-12 {
			refine = 0.5 * (y0 - y2) / den
			if refine > 1 || refine < -1 {
				refine = 0
			}
		}
	}
	return (float64(bestK) + refine) / calSampleRate, cos
}

func corrAt(x []complex128, k, size int) float64 {
	if k < 0 {
		k = size + k
	}
	return real(x[k])
}

func conj(v complex128) complex128 { return complex(real(v), -imag(v)) }

// fft is an in-place iterative radix-2 Cooley-Tukey transform.
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		step := complex(math.Cos(angle), math.Sin(angle))
		for i := 0; i < n; i += length {
			w := complex(1, 0)
			for k := 0; k < length/2; k++ {
				u := a[i+k]
				v := a[i+k+length/2] * w
				a[i+k] = u + v
				a[i+k+length/2] = u - v
				w *= step
			}
		}
	}
}

// inverseFFT conjugates, transforms, conjugates and scales.
func inverseFFT(a []complex128) {
	for i := range a {
		a[i] = conj(a[i])
	}
	fft(a)
	scale := complex(1/float64(len(a)), 0)
	for i := range a {
		a[i] = conj(a[i]) * scale
	}
}

// extractCalibrationPCM decodes the reference audio (the language-ref
// track when present, else the first) to mono f32le at the measurement
// rate.
func (t *tool) extractCalibrationPCM(ref string, info *probe.Info) ([]float32, error) {
	track := 0
	if audios := info.ByType()[probe.TypeAudio]; len(audios) > 0 {
		wanted := media.ExpandWhitelist([]string{t.cfg.LangRef})
		for _, a := range audios {
			if media.LangMatches(a.Stream.Tags.Language, wanted) {
				track = a.Ordinal
				break
			}
		}
	}
	raw, err := t.r.Output(t.ffmpegBin, "-v", "error", "-nostdin", "-i", ref,
		"-map", fmt.Sprintf("0:a:%d", track),
		"-ac", "1", "-ar", fmt.Sprint(calSampleRate), "-f", "f32le", "pipe:1")
	if err != nil {
		return nil, fmt.Errorf("cannot decode the reference audio for calibration: %w", err)
	}
	pcm := make([]float32, len(raw)/4)
	for i := range pcm {
		pcm[i] = float32(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
	}
	return pcm, nil
}

// stereoToCalPCM reads a raw s16le stereo file and converts it to mono
// at the measurement rate.
func stereoToCalPCM(path string) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the resampled audio: %w", err)
	}
	frames := len(raw) / 4
	// average the stereo pair, then box-decimate to the measurement rate
	step := sampleRate / calSampleRate
	out := make([]float32, 0, frames/step+1)
	for f := 0; f+step <= frames; f += step {
		sum := 0.0
		for k := 0; k < step; k++ {
			l := int16(binary.LittleEndian.Uint16(raw[(f+k)*4:]))
			r := int16(binary.LittleEndian.Uint16(raw[(f+k)*4+2:]))
			sum += float64(l) + float64(r)
		}
		out = append(out, float32(sum/float64(2*step)/32768))
	}
	return out, nil
}
