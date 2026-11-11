// Package encoders probes working video encoders and builds the per-mode
// ffmpeg video arguments. The probe order covers NVIDIA (nvenc), Intel
// (qsv), Intel and AMD GPUs via VAAPI (Mesa) and a software encoder; the
// encoder is chosen at runtime on every machine. Each backend exists for
// both output codecs (HEVC and AV1) under its ffmpeg name: hevc_nvenc vs
// av1_nvenc, libx265 vs libsvtav1.
package encoders

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/hranicka/mediatool/internal/run"
)

// Codec identifies the output video codec a conversion targets.
type Codec string

const (
	CodecHEVC Codec = "hevc"
	CodecAV1  Codec = "av1"
)

// ParseCodec validates a -c output codec value.
func ParseCodec(s string) (Codec, error) {
	switch Codec(s) {
	case CodecHEVC, CodecAV1:
		return Codec(s), nil
	}
	return "", fmt.Errorf("unknown output codec %q (want hevc or av1)", s)
}

// Kind identifies an encoder backend (hardware family or software).
type Kind string

const (
	NVENC Kind = "nvenc"
	QSV   Kind = "qsv"
	VAAPI Kind = "vaapi"
	SW    Kind = "sw"
)

// Candidates returns the probe order for a target codec; both codecs
// exist for every backend.
func Candidates(c Codec) []Kind { return []Kind{NVENC, QSV, VAAPI, SW} }

// ParseKind validates an -e encoder value; "x265" is a deprecated alias
// for "sw".
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case NVENC, QSV, VAAPI, SW:
		return Kind(s), nil
	case "x265":
		return SW, nil
	}
	return "", fmt.Errorf("unknown encoder %q (want nvenc, qsv, vaapi or sw)", s)
}

var kindLabels = map[Kind]string{
	NVENC: "Nvidia NVENC",
	QSV:   "Intel QuickSync",
	VAAPI: "VAAPI (Intel/AMD)",
}

// Label returns the human-readable name of a codec/backend pair; the
// software backend names the actual encoder (libx265 vs libsvtav1).
func Label(c Codec, k Kind) string {
	if k == SW {
		if c == CodecAV1 {
			return "software libsvtav1"
		}
		return "software libx265"
	}
	return kindLabels[k]
}

// encoderName is the ffmpeg -c:v value for a codec/backend pair.
func encoderName(c Codec, k Kind) string {
	if c == CodecAV1 {
		switch k {
		case NVENC:
			return "av1_nvenc"
		case QSV:
			return "av1_qsv"
		case VAAPI:
			return "av1_vaapi"
		default:
			return "libsvtav1"
		}
	}
	switch k {
	case NVENC:
		return "hevc_nvenc"
	case QSV:
		return "hevc_qsv"
	case VAAPI:
		return "hevc_vaapi"
	default:
		return "libx265"
	}
}

// Encoder is a working encoder backend on this machine.
type Encoder struct {
	Codec Codec
	Kind  Kind
	Label string
}

func (e Encoder) name() string { return encoderName(e.Codec, e.Kind) }

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

// cappedPlan returns the bitrate plan for the capped modes: camera
// targets ~55 % of the source bitrate capped at 65 %, lowq targets ~40 %
// capped at 50 %. Sources whose cap would fall below a sane minimum get
// no cap at all (pure quality targeting; the size gate bounds the
// damage) — floor-clamping instead pushed the cap above the source
// bitrate, where it constrained nothing.
func cappedPlan(m Mode, srcKbps int) (tgt, cap int) {
	tgtPct, capPct, minCap := 55, 65, 800
	if m == ModeLowQ {
		tgtPct, capPct, minCap = 40, 50, 600
	}
	tgt, cap = srcKbps*tgtPct/100, srcKbps*capPct/100
	if cap < minCap {
		return 0, 0
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

// CUDADecodeArgs keeps decoded frames on the GPU so the CUDA-only filter
// chain (scale_cuda/yadif_cuda/tonemap_cuda) can run before a hardware
// encode.
func (e Encoder) CUDADecodeArgs() []string {
	return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
}

// HasFilter probes whether the ffmpeg build supports a filter, using a
// tiny lavfi encode (same pattern as the encoder probe).
func HasFilter(r run.Runner, ffmpegBin, filter string) bool {
	args := []string{"-hide_banner", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=c=black:s=320x240:d=0.2:r=30", "-frames:v", "5",
		"-vf", filter, "-f", "null", "-"}
	_, err := r.Output(ffmpegBin, args...)
	return err == nil
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
		// av1_nvenc shares the hevc_nvenc option set; temporal-AQ + lookahead
		// + fullres multipass spend bits better on motion and grain
		return []string{"-c:v", e.name(), "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", cqStr, "-b:v", "0", "-spatial-aq", "1", "-temporal-aq", "1", "-rc-lookahead", "32", "-multipass", "2"}
	case QSV:
		return []string{"-c:v", e.name(), "-preset", "medium", "-global_quality", cqStr}
	case VAAPI:
		// CQP works on both Intel and Mesa (AMD); ICQ would be Intel-only.
		return []string{"-c:v", e.name(), "-rc_mode", "CQP", "-qp", cqStr}
	default:
		if e.Codec == CodecAV1 {
			return []string{"-c:v", "libsvtav1", "-preset", "8", "-crf", cqStr}
		}
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
		return []string{"-c:v", e.name(), "-preset", "medium", "-rc", "vbr", "-b:v", tgtStr, "-maxrate", capStr, "-bufsize", bufStr}
	case VAAPI:
		return []string{"-c:v", e.name(), "-rc_mode", "VBR", "-b:v", tgtStr, "-maxrate", capStr, "-bufsize", bufStr}
	default:
		if e.Codec == CodecAV1 {
			return []string{"-c:v", "libsvtav1", "-preset", "8", "-crf", fmt.Sprint(cq), "-maxrate", capStr, "-bufsize", bufStr}
		}
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", fmt.Sprint(cq), "-maxrate", capStr, "-bufsize", bufStr}
	}
}

// chromecastArgs builds the Chromecast-safe encoder block; the level cap is
// enforced on NVENC only (parity with the bash suite). Chromecast 4
// devices decode HEVC only, so this block is HEVC-specific — convert
// rejects the av1 codec in chromecast mode.
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
		if tgt, cap := cappedPlan(m, srcVideoKbps); cap > 0 {
			return e.cappedArgs(m, cq, tgt, cap)
		}
		return e.qualityArgs(cq)
	}
	panic("unreachable mode " + string(m))
}

// PlanInfo summarizes the conversion plan for one file, mirroring the bash
// CAP_INFO line. The chromecast ceiling note is honest about VAAPI: CQP
// cannot carry a maxrate, so the cap is enforced encoder-side only where
// supported.
func (e Encoder) PlanInfo(m Mode, cq, srcVideoKbps int) string {
	switch m {
	case ModeMovie:
		return fmt.Sprintf("CQ %d quality target", cq)
	case ModeChromecast:
		if e.Kind == VAAPI {
			return fmt.Sprintf("CQ %d quality target (vaapi CQP has no bitrate ceiling)", cq)
		}
		return fmt.Sprintf("CQ %d, 15M cap, level 4.1", cq)
	default:
		if _, cap := cappedPlan(m, srcVideoKbps); cap > 0 {
			return fmt.Sprintf("CQ %d, capped at %dk (source %dk)", cq, cap, srcVideoKbps)
		}
		return fmt.Sprintf("CQ %d quality target (no cap)", cq)
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
// candidate is verified with a real test encode for the given output
// codec.
func Probe(r run.Runner, ffmpegBin, vaapiDevice string, c Codec, candidates []Kind) (Encoder, error) {
	for _, k := range candidates {
		e := Encoder{Codec: c, Kind: k, Label: Label(c, k)}
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
