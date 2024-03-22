// Package hevc provides conversion to HEVC codec.
package hevc

import (
	"fmt"
	"github.com/hranicka/mediatool/internal"
	"os"
	"strconv"
	"strings"
)

const (
	EncQualityTypeAuto = "auto"
	EncQualityTypeQP   = "qp"
)

var (
	VaapiDevice       = "/dev/dri/renderD128"
	EncQualityPercent = 0.6
	EncQualityPreset  = 20
	EncQualityType    = EncQualityTypeAuto
)

func Process(src string, dryRun bool, del bool) error {
	// read file streams
	internal.LogInfo("opening file %s", src)
	f, err := internal.Probe(src)
	if err != nil {
		return fmt.Errorf("cannot get file info: %v", err)
	}

	fileBitrate, err := strconv.Atoi(f.Format.BitRate)
	if err != nil {
		return fmt.Errorf("cannot get file bitrate: %v", err)
	}

	// add type-specific stream counters
	cnt := make(map[string]int)
	var bitrateSum int
	for i, s := range f.Streams {
		if _, ok := cnt[s.CodecType]; ok {
			cnt[s.CodecType]++
		} else {
			cnt[s.CodecType] = 0
		}
		f.Streams[i].TypeIndex = cnt[s.CodecType]

		if s.BitRate != "" {
			br, _ := strconv.Atoi(s.BitRate)
			bitrateSum += br
		}
	}

	// detect streams for conversion
	var toConvert []internal.Stream
	for _, s := range f.Streams {
		if s.CodecType != internal.TypeVideo {
			continue
		}

		// add missing bitrate
		if s.BitRate == "" {
			s.BitRate = strconv.Itoa(fileBitrate - bitrateSum)
		}

		switch s.CodecName {
		case internal.CodecH264:
			toConvert = append(toConvert, s)
		}
	}

	// convert if needed
	if len(toConvert) == 0 {
		internal.LogInfo("no conversion needed, nothing to convert")
	} else if len(toConvert) > 1 {
		internal.LogError("multiple video streams detected, cannot convert")
	} else {
		internal.LogInfo("converting %d track(s)", len(toConvert))
		internal.LogDebug("%+v", toConvert)

		if !dryRun {
			dst := src + ".tmp.mkv" // TODO Validate original extension
			if err := convert(src, dst, toConvert); err != nil {
				return fmt.Errorf("cannot convert file: %v", err)
			}

			old := src + ".old"
			if err := os.Rename(src, old); err != nil {
				return fmt.Errorf("cannot rename source file: %v", err)
			}
			if err := os.Rename(dst, src); err != nil {
				return fmt.Errorf("cannot rename converted file: %v", err)
			}

			if del {
				if err := os.Remove(old); err != nil {
					return fmt.Errorf("cannot delete source file: %v", err)
				}
			}
		}
	}

	internal.LogInfo("file finished\n")
	return nil
}

func convert(src string, dst string, streams []internal.Stream) error {
	var args []string
	args = append(args, "-vaapi_device", VaapiDevice)
	args = append(args, "-i", src)
	args = append(args, "-vf", "format=nv12,hwupload")
	args = append(args, "-map", "0")

	for _, s := range streams {
		args = append(args, fmt.Sprintf("-c:v:%d", s.TypeIndex), "hevc_vaapi")

		br, _ := strconv.Atoi(s.BitRate)
		if EncQualityType == EncQualityTypeAuto && br > 0 {
			args = append(args, fmt.Sprintf("-b:v:%d", s.TypeIndex), fmt.Sprintf("%.0fk", (float64(br)/1024)*EncQualityPercent))
		} else {
			args = append(args, "-qp", fmt.Sprintf("%d", EncQualityPreset))
		}

		args = append(args, "-low_power", "1")
	}

	args = append(args, "-c:a", "copy")
	args = append(args, "-c:s", "copy")
	args = append(args, "-max_muxing_queue_size", "4096")
	args = append(args, dst)

	internal.LogDebug(fmt.Sprintf("running: %s %v\n", internal.FFmpegPath, strings.Join(args, " ")))

	_, err := internal.RunCmd(internal.FFmpegPath, args...)
	return err
}
