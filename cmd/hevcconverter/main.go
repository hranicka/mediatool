package main

import (
	"flag"
	"github.com/hranicka/mediatool/internal/hevc"
	"log/slog"
	"os"
	"strings"

	"github.com/hranicka/mediatool/internal"
)

func main() {
	// parse cli args
	verbose := flag.Bool("v", false, "verbose/debug output")
	flag.StringVar(&internal.FFmpegPath, "ffmpeg", "ffmpeg", "ffmpeg path")
	flag.StringVar(&hevc.VaapiDevice, "vaapi_device", "/dev/dri/renderD128", "ffmpeg vaapi_device")
	flag.Float64Var(&hevc.EncQualityPercent, "quality_percent", 0.6, "percentage bitrate quality according to source")
	flag.IntVar(&hevc.EncQualityPreset, "quality_preset", 20, "static quality preset (qp) passed to ffmpeg")
	flag.StringVar(&hevc.EncQualityType, "quality_type", hevc.EncQualityTypeAuto, "encoding quality type (auto/qp)")
	flag.IntVar(&hevc.EncBitrate, "bitrate", 0, "encoding quality bitrate (kbps)")

	var file string
	var dir string
	var del bool
	var dryRun bool
	var ignore string
	flag.StringVar(&file, "file", "", "source file path (cannot be combined with -dir)")
	flag.StringVar(&dir, "dir", "", "source files directory (cannot be combined with -file)")
	flag.BoolVar(&del, "del", false, "delete source files after successful conversion")
	flag.BoolVar(&dryRun, "dry", false, "run in dry mode = without actual conversion")
	flag.StringVar(&ignore, "ignore", "", "comma separated list of substrings to ignore")

	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// validate
	if (file == "" && dir == "") || (file != "" && dir != "") {
		flag.PrintDefaults()
		return
	}

	// run
	if dryRun {
		slog.Debug("DRY RUN")
	}

	if file != "" {
		if err := hevc.Process(file, dryRun, del); err != nil {
			slog.Error("could not process", "file", file, "error", err)
		}
	}

	if dir != "" {
		ignores := internal.FileIgnoresWhenExist(dir + "/.hevcconverter-ignore")
		if ignore != "" {
			ignores = append(ignores, strings.Split(ignore, ",")...)
		}

		internal.Walk(dir, ignores, func(path string, info os.FileInfo) {
			if err := hevc.Process(path, dryRun, del); err != nil {
				slog.Error("could not process", "file", path, "error", err)
			}
		})
	}
}
