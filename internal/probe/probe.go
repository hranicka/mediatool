// Package probe parses ffprobe JSON output into a media file model.
package probe

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hranicka/media-tool/internal/run"
)

// Stream types used throughout the tool suite.
const (
	TypeVideo    = "video"
	TypeAudio    = "audio"
	TypeSubtitle = "subtitle"
)

type Tags struct {
	Language string `json:"language"`
	Title    string `json:"title"`
}

type Disposition struct {
	AttachedPic int `json:"attached_pic"`
}

// SideData is one entry of a stream's side_data_list; only the type is
// needed (e.g. "DOVI configuration record" for Dolby Vision detection).
type SideData struct {
	SideDataType string `json:"side_data_type"`
}

type Stream struct {
	Index          int         `json:"index"`
	CodecName      string      `json:"codec_name"`
	CodecType      string      `json:"codec_type"`
	PixFmt         string      `json:"pix_fmt"`
	Width          int         `json:"width"`
	Height         int         `json:"height"`
	Channels       int         `json:"channels"`
	SampleRate     string      `json:"sample_rate"`
	BitRate        string      `json:"bit_rate"`
	RFrameRate     string      `json:"r_frame_rate"`
	ColorPrimaries string      `json:"color_primaries"`
	ColorTransfer  string      `json:"color_trc"`
	ColorSpace     string      `json:"color_space"`
	Tags           Tags        `json:"tags"`
	Disposition    Disposition `json:"disposition"`
	SideDataList   []SideData  `json:"side_data_list"`
}

// BitRateInt returns the stream bitrate as a number (0 when ffprobe omits
// it, which is common for VBR tracks in Matroska).
func (s Stream) BitRateInt() int {
	n, _ := strconv.Atoi(s.BitRate)
	return n
}

type Format struct {
	// FormatName is the ffprobe container name list (e.g. "matroska,webm",
	// "mov,mp4,m4a,3gp,3g2,mj2").
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
	BitRate    string `json:"bit_rate"`
}

// Info is the parsed ffprobe output of one file.
type Info struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

// Typed couples a stream with its per-type ordinal — the 0-based position
// among the streams of the same codec_type, matching ffmpeg's :N stream
// specifiers.
type Typed struct {
	Stream  Stream
	Ordinal int
}

// Probe runs ffprobe against src and parses the JSON output.
func Probe(r run.Runner, ffprobeBin, src string) (*Info, error) {
	out, err := r.Output(ffprobeBin, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", src)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	slog.Debug("probed file", "src", src, "output", string(out))

	info := &Info{}
	if err := json.Unmarshal(out, info); err != nil {
		return nil, fmt.Errorf("cannot parse ffprobe output: %w", err)
	}
	return info, nil
}

// ByType groups streams by codec_type, each carrying its per-type ordinal.
func (i *Info) ByType() map[string][]Typed {
	res := make(map[string][]Typed)
	for _, s := range i.Streams {
		res[s.CodecType] = append(res[s.CodecType], Typed{Stream: s, Ordinal: len(res[s.CodecType])})
	}
	return res
}

// Duration returns the container duration in seconds (false when missing
// or unparseable).
func (i *Info) Duration() (float64, bool) {
	d, err := strconv.ParseFloat(i.Format.Duration, 64)
	return d, err == nil
}

// Size returns the container size in bytes (false when missing).
func (i *Info) Size() (int64, bool) {
	s, err := strconv.ParseInt(i.Format.Size, 10, 64)
	return s, err == nil
}

// IsDolbyVision reports whether any video stream carries a DOVI side-data
// record (the DV layer cannot survive a re-encode). Matched by the "DOVI"
// prefix to stay robust across ffprobe version wording.
func (i *Info) IsDolbyVision() bool {
	return i.videoSideDataContains("DOVI")
}

// IsHDR10Plus reports whether any video stream carries HDR10+ dynamic
// metadata (which a re-encode drops, static HDR10 survives).
func (i *Info) IsHDR10Plus() bool {
	return i.videoSideDataContains("HDR10+")
}

func (i *Info) videoSideDataContains(needle string) bool {
	for _, s := range i.Streams {
		if s.CodecType != TypeVideo {
			continue
		}
		for _, sd := range s.SideDataList {
			if strings.Contains(sd.SideDataType, needle) {
				return true
			}
		}
	}
	return false
}

// Is10Bit reports whether the stream uses a 10-bit pixel format.
func (s Stream) Is10Bit() bool {
	return strings.Contains(s.PixFmt, "10")
}
