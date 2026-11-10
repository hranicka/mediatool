// Package encoders probes working HEVC encoders and builds the per-mode
// ffmpeg video arguments. The probe order covers NVIDIA (nvenc), Intel
// (qsv), Intel and AMD GPUs via VAAPI (Mesa) and software libx265; the
// encoder is chosen at runtime on every machine.
package encoders

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/hranicka/media-tool/internal/run"
)

// Kind identifies an HEVC encoder backend.
type Kind string

const (
	NVENC Kind = "nvenc"
	QSV   Kind = "qsv"
	VAAPI Kind = "vaapi"
	X265  Kind = "x265"
)

// Candidates returns the default probe order.
func Candidates() []Kind { return []Kind{NVENC, QSV, VAAPI, X265} }

// ParseKind validates a -e encoder value.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case NVENC, QSV, VAAPI, X265:
		return Kind(s), nil
	}
	return "", fmt.Errorf("unknown encoder %q (want nvenc, qsv, vaapi or x265)", s)
}

var labels = map[Kind]string{
	NVENC: "Nvidia NVENC",
	QSV:   "Intel QuickSync",
	VAAPI: "VAAPI (Intel/AMD)",
	X265:  "software libx265",
}

// Encoder is a working HEVC encoder backend on this machine.
type Encoder struct {
	Kind  Kind
	Label string
}

// Mode selects the conversion quality strategy (ported from the bash
// convert-video.sh): quality targets with optional source-bitrate caps.
type Mode string

const (
	ModeMovie      Mode = "movie"
	ModeCamera     Mode = "camera"
	ModeLowQ       Mode = "lowq"
	ModeChromecast Mode = "chromecast"
)

// DefaultCQ returns the mode's default quality value.
func (m Mode) DefaultCQ() int {
	switch m {
	case ModeCamera:
		return 28
	case ModeLowQ:
		return 31
	case ModeChromecast:
		return 22
	default:
		return 26
	}
}

// ParseMode validates a -p mode value.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeMovie, ModeCamera, ModeLowQ, ModeChromecast:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown mode %q (want movie, camera, lowq or chromecast)", s)
}

// cappedPlan mirrors the bash bitrate math: camera targets ~55 % capped at
// 65 % of the source, lowq targets ~40 % capped at 50 %, with floors.
func cappedPlan(m Mode, srcKbps int) (tgt, cap int) {
	tgtPct, capPct, tgtMin, capMin := 55, 65, 500, 800
	if m == ModeLowQ {
		tgtPct, capPct, tgtMin, capMin = 40, 50, 400, 600
	}
	tgt = srcKbps * tgtPct / 100
	if tgt < tgtMin {
		tgt = tgtMin
	}
	cap = srcKbps * capPct / 100
	if cap < capMin {
		cap = capMin
	}
	return tgt, cap
}

// InputArgs returns arguments that must precede the -i input option
// (VAAPI device initialization); other encoders need none.
func (e Encoder) InputArgs(vaapiDevice string) []string {
	if e.Kind == VAAPI {
		return []string{"-vaapi_device", vaapiDevice}
	}
	return nil
}

// Filters returns the source-side filter fragments the encoder requires
// (VAAPI uploads frames to the GPU; 10-bit sources must become p010,
// everything else nv12). Callers merge them with their own scale filter
// into a single -vf chain.
func (e Encoder) Filters(is10bit bool) []string {
	if e.Kind != VAAPI {
		return nil
	}
	if is10bit {
		return []string{"format=p010le", "hwupload"}
	}
	return []string{"format=nv12", "hwupload"}
}

// PixFmtArgs returns the pixel-format arguments for non-VAAPI encoders
// (10-bit sources request p010le output; VAAPI handles it via the format
// filter instead).
func (e Encoder) PixFmtArgs(is10bit bool) []string {
	if e.Kind == VAAPI || !is10bit {
		return nil
	}
	return []string{"-pix_fmt", "p010le"}
}

// qualityArgs builds the quality-targeted encoder block (bash CAP_INFO
// "CQ N quality target").
func (e Encoder) qualityArgs(cq int) []string {
	cqStr := fmt.Sprint(cq)
	switch e.Kind {
	case NVENC:
		return []string{"-c:v", "hevc_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", cqStr, "-b:v", "0", "-spatial-aq", "1"}
	case QSV:
		return []string{"-c:v", "hevc_qsv", "-preset", "medium", "-global_quality", cqStr}
	case VAAPI:
		// CQP works on both Intel and Mesa (AMD); ICQ would be Intel-only.
		return []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", cqStr}
	default:
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", cqStr}
	}
}

// cappedArgs builds the bitrate-capped encoder block (bash camera/lowq
// modes with a known source bitrate).
func (e Encoder) cappedArgs(m Mode, cq, tgt, cap int) []string {
	tgtStr := fmt.Sprintf("%dk", tgt)
	capStr := fmt.Sprintf("%dk", cap)
	bufStr := fmt.Sprintf("%dk", cap*2)
	switch e.Kind {
	case NVENC:
		// quality targeting with a hard ceiling
		args := e.qualityArgs(cq)
		return append(args, "-maxrate", capStr, "-bufsize", bufStr)
	case QSV:
		return []string{"-c:v", "hevc_qsv", "-preset", "medium", "-rc", "vbr", "-b:v", tgtStr, "-maxrate", capStr, "-bufsize", bufStr}
	case VAAPI:
		return []string{"-c:v", "hevc_vaapi", "-rc_mode", "VBR", "-b:v", tgtStr, "-maxrate", capStr, "-bufsize", bufStr}
	default:
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", fmt.Sprint(cq), "-maxrate", capStr, "-bufsize", bufStr}
	}
}

// chromecastArgs builds the Chromecast-safe encoder block; the level cap is
// enforced on NVENC only (parity with the bash suite).
func (e Encoder) chromecastArgs(cq int) []string {
	cqStr := fmt.Sprint(cq)
	switch e.Kind {
	case NVENC:
		return []string{"-c:v", "hevc_nvenc", "-preset", "p6", "-tune", "hq", "-cq", cqStr, "-maxrate", "15M", "-bufsize", "15M", "-profile:v", "main", "-level", "4.1", "-tier", "main"}
	case QSV:
		return []string{"-c:v", "hevc_qsv", "-preset", "medium", "-global_quality", cqStr, "-maxrate", "15M", "-bufsize", "15M"}
	case VAAPI:
		// CQP cannot carry a bitrate ceiling; at 1080p CQP stays well
		// below the Chromecast cap for typical content.
		return []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", cqStr}
	default:
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", cqStr, "-maxrate", "15M", "-bufsize", "15M"}
	}
}

// VideoArgs builds the encoder arguments for one output video stream.
// srcVideoKbps is the source video bitrate (0 = unknown); the capped modes
// then fall back to pure quality targeting, mirroring the bash behavior.
func (e Encoder) VideoArgs(m Mode, cq, srcVideoKbps int) []string {
	switch m {
	case ModeMovie, ModeChromecast:
		if m == ModeMovie {
			return e.qualityArgs(cq)
		}
		return e.chromecastArgs(cq)
	case ModeCamera, ModeLowQ:
		if srcVideoKbps > 0 {
			tgt, cap := cappedPlan(m, srcVideoKbps)
			return e.cappedArgs(m, cq, tgt, cap)
		}
		return e.qualityArgs(cq)
	}
	panic("unreachable mode " + string(m))
}

// PlanInfo summarizes the conversion plan for one file, mirroring the bash
// CAP_INFO line.
func (m Mode) PlanInfo(cq, srcVideoKbps int) string {
	switch m {
	case ModeMovie:
		return fmt.Sprintf("CQ %d quality target", cq)
	case ModeChromecast:
		return fmt.Sprintf("CQ %d, 15M cap, level 4.1", cq)
	default:
		if srcVideoKbps > 0 {
			_, cap := cappedPlan(m, srcVideoKbps)
			return fmt.Sprintf("CQ %d, capped at %dk (source %dk)", cq, cap, srcVideoKbps)
		}
		return fmt.Sprintf("CQ %d quality target (source bitrate unknown, no cap)", cq)
	}
}

// probeArgs builds a minimal real encode used to test one encoder.
func (e Encoder) probeArgs(vaapiDevice string) []string {
	args := []string{"-hide_banner", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=c=black:s=320x240:d=0.2:r=30", "-frames:v", "5"}
	args = append(args, e.InputArgs(vaapiDevice)...)
	if f := e.Filters(false); len(f) > 0 {
		args = append(args, "-vf", strings.Join(f, ","))
	}
	args = append(args, e.qualityArgs(26)...)
	return append(args, "-f", "null", "-")
}

// Probe returns the first working encoder among the candidates; every
// candidate is verified with a real test encode.
func Probe(r run.Runner, ffmpegBin, vaapiDevice string, candidates []Kind) (Encoder, error) {
	for _, k := range candidates {
		e := Encoder{Kind: k, Label: labels[k]}
		args := e.probeArgs(vaapiDevice)
		slog.Debug("probing encoder", "kind", k, "cmd", ffmpegBin+" "+strings.Join(args, " "))
		if _, err := r.Output(ffmpegBin, args...); err != nil {
			slog.Debug("encoder probe failed", "kind", k, "err", err)
			continue
		}
		return e, nil
	}
	return Encoder{}, fmt.Errorf("no working encoder among %v", candidates)
}
