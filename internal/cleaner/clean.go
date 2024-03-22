// Package cleaner removes unwanted streams from media files.
package cleaner

import (
	"fmt"
	"github.com/hranicka/mediatool/internal"
	"os"
	"strings"
)

var (
	whitelistLang = []string{
		"", "und", "unknown",
		"english", "eng", "en",
		"czech", "cze", "ces", "cz", "cs",
	}
)

func Process(src string, dryRun bool, del bool) error {
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
	var toRemove []internal.Stream
	for _, s := range f.Streams {
		if s.CodecType == internal.TypeVideo {
			// remove images since they act as video after ffmpeg conversion
			if s.CodecName == "mjpeg" {
				toRemove = append(toRemove, s)
			}
			continue
		}

		// keep if it's the only track of the type
		if cnt[s.CodecType] == 1 {
			continue
		}

		// remove unwanted languages
		var whitelisted bool
		for _, lang := range whitelistLang {
			if strings.EqualFold(s.Tags.Language, lang) {
				whitelisted = true
				break
			}
		}

		if !whitelisted {
			toRemove = append(toRemove, s)
		}
	}

	// convert if needed
	if len(toRemove) == 0 {
		internal.LogInfo("no cleanup needed")
	} else {
		internal.LogInfo("removing %d track(s)", len(toRemove))
		internal.LogDebug("%+v", toRemove)

		if !dryRun {
			dst := src + ".tmp.mkv" // TODO Validate original extension
			if err := cleanup(src, dst, toRemove); err != nil {
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

func cleanup(src string, dst string, streams []internal.Stream) error {
	var args []string
	args = append(args, "-i", src)

	// preserve just video, audio and subtitles
	args = append(args, "-map", "0:v")
	args = append(args, "-map", "0:a")
	args = append(args, "-map", "0:s")

	for _, s := range streams {
		var t string
		switch s.CodecType {
		case internal.TypeVideo:
			t = "v"
		case internal.TypeAudio:
			t = "a"
		case internal.TypeSubtitles:
			t = "s"
		default:
			return fmt.Errorf("unsupported stream type: %s", s.CodecType)
		}

		args = append(args, "-map", fmt.Sprintf("-0:%s:%d", t, s.TypeIndex))
	}

	args = append(args, "-c", "copy")
	args = append(args, "-map_metadata:g", "0:g") // remove additional metadata
	args = append(args, "-max_muxing_queue_size", "4096")
	args = append(args, dst)

	internal.LogDebug(fmt.Sprintf("running: %s %v\n", internal.FFmpegPath, strings.Join(args, " ")))

	_, err := internal.RunCmd(internal.FFmpegPath, args...)
	return err
}
