// Package ac3 provides conversion to AC3 codec.
package ac3

import (
	"fmt"
	"github.com/hranicka/mediatool/internal"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var (
	commentRegExp = regexp.MustCompile(`(?i)(?:comment|director)`)
)

func Process(src string, lang string, minBitRate int, dryRun bool, del bool) error {
	// read file streams
	slog.Debug("opening file", "path", src)
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
				slog.Debug("low bitrate or commentary stream, skipping", "file", src, "stream", s)
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
			slog.Debug("already converted stream, skipping", "file", src, "stream", l)
			continue
		}

		if l == lang {
			hasLang = true
		}
		toConvert = append(toConvert, bs)
		slog.Debug("stream for conversion found (codec %s)", "file", src, "stream", l, "codec", bs.CodecName)
	}

	// convert if needed
	if len(toConvert) == 0 {
		slog.Debug("no conversion needed, nothing to convert", "file", src)
	} else if !hasLang {
		slog.Debug("no conversion needed, does not contain language", "file", src, "lang", lang)
	} else {
		slog.Info("converting tracks", "file", src, "cnt", len(toConvert), "streams", toConvert)

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

	slog.Debug("file finished", "file", src)
	return nil
}

func convert(src string, dst string, streams []internal.Stream) error {
	var args []string
	args = append(args, "-i", src)
	args = append(args, "-map", "0")
	args = append(args, "-c:v", "copy")
	args = append(args, "-c:a", "copy")

	for _, s := range streams {
		args = append(args, fmt.Sprintf("-c:a:%d", s.TypeIndex), "ac3", fmt.Sprintf("-b:a:%d", s.TypeIndex), "640k")
	}

	args = append(args, "-c:s", "copy")
	args = append(args, "-max_muxing_queue_size", "4096")
	args = append(args, dst)

	slog.Debug("running ffmpeg", "file", src, "cmd", fmt.Sprintf("%s %v\n", internal.FFmpegPath, strings.Join(args, " ")))

	_, err := internal.RunCmd(internal.FFmpegPath, args...)
	return err
}
