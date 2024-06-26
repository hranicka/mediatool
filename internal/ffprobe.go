package internal

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

const (
	// TypeAudio is an audio stream
	TypeAudio = "audio"
	// CodecDTS is a DTS codec
	CodecDTS = "dts"
	// CodecTrueHD is a CodecTrueHD codec
	CodecTrueHD = "truehd"
	// CodecFLAC is a FLAC codec
	CodecFLAC = "flac"
	// CodecAC3 is an AC3 codec
	CodecAC3 = "ac3"
	// CodecEAC3 is an E-AC3 codec
	CodecEAC3 = "eac3"
)

const (
	// TypeVideo is a video stream
	TypeVideo = "video"
	// CodecH264 is an H264 codec
	CodecH264 = "h264"
)

const (
	// TypeSubtitles is a subtitle stream
	TypeSubtitles = "subtitle"
)

type Tags struct {
	Language string `json:"language"`
	Title    string `json:"title"`
}

type Stream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	BitRate   string `json:"bit_rate"`
	Channels  int    `json:"channels"`
	Tags      Tags   `json:"tags"`
	TypeIndex int
}

type Format struct {
	Duration string `json:"duration"`
	Size     string `json:"size"`
	BitRate  string `json:"bit_rate"`
}

type FFprobe struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

func Probe(src string) (*FFprobe, error) {
	out, err := RunCmd("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", src)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %v", err)
	}
	slog.Debug("probing file", "src", src, "output", string(out))

	f := &FFprobe{}
	if err := json.Unmarshal(out, f); err != nil {
		return nil, fmt.Errorf("cannot parse %s output: %v", "ffprobe", err)
	}

	return f, nil
}
