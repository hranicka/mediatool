// Package ac3 provides conversion to AC3 codec.
package ac3

import (
	"fmt"
	"github.com/hranicka/mediatool/internal"
	"os"
	"regexp"
	"strconv"
)

var (
	commentRegExp = regexp.MustCompile(`(?i)(?:comment|director)`)
)

func Process(src string, lang string, minBitRate int, dryRun bool, del bool) error {
	// read file streams
	internal.LogInfo("opening file %s", src)
	f, err := internal.Probe(src)
	if err != nil {
		return fmt.Errorf("cannot get file info: %v", err)
	}

	// add type-specific stream counters
	cnt := make(map[string]int)
	for i, s := range f.Streams {
		if _, ok := cnt[s.CodecType]; ok {
			cnt[s.CodecType]++
		} else {
			cnt[s.CodecType] = 0
		}
		f.Streams[i].TypeIndex = cnt[s.CodecType]
	}

	// detect streams for conversion
	valid := make(map[string]internal.Stream)
	bad := make(map[string]internal.Stream)
	for _, s := range f.Streams {
		if s.CodecType != internal.TypeAudio {
			continue
		}

		switch s.CodecName {
		case internal.CodecAC3:
			// exclude commentary and low bitrate tracks
			bitRate, _ := strconv.Atoi(s.BitRate)
			if commentRegExp.MatchString(s.Tags.Title) || (bitRate > 0 && bitRate < minBitRate) || (bitRate == 0 && s.Channels < 6) {
				internal.LogDebug("> low bitrate or commentary stream, skipping: %+v", s)
				break
			}
			valid[s.Tags.Language] = s
		case internal.CodecDTS, internal.CodecTrueHD, internal.CodecFLAC, internal.CodecEAC3:
			bad[s.Tags.Language] = s
		}
	}

	hasLang := lang == ""
	var toConvert []internal.Stream
	for l, bs := range bad {
		if _, ok := valid[l]; ok {
			internal.LogInfo("> %s: already converted stream, skipping", l)
			continue
		}

		if l == lang {
			hasLang = true
		}
		toConvert = append(toConvert, bs)
		internal.LogInfo("> %s: stream for conversion found (codec %s)", l, bs.CodecName)
	}

	// convert if needed
	if len(toConvert) == 0 {
		internal.LogInfo("no conversion needed, nothing to convert")
	} else if !hasLang {
		internal.LogInfo("no conversion needed, does not contain language %s", lang)
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
	// TODO Assumes all streams are audio

	args := []string{"-i", src, "-map", "0:v", "-map", "0:a"}
	for _, s := range streams {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", s.TypeIndex))
	}
	args = append(args, "-map", "0:s?", "-c", "copy")
	for _, s := range streams {
		args = append(args, fmt.Sprintf("-c:a:%d", s.TypeIndex), "ac3", fmt.Sprintf("-b:a:%d", s.TypeIndex), "640k")
	}
	args = append(args, "-max_muxing_queue_size", "4096", dst)

	_, err := internal.RunCmd("ffmpeg", args...)
	return err
}
