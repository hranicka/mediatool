package main

import (
	"flag"
	"github.com/hranicka/mediatool/internal/hevc"
	"os"

	"github.com/hranicka/mediatool/internal"
)

func main() {
	// parse cli args
	flag.BoolVar(&internal.Verbose, "v", false, "verbose/debug output")
	flag.StringVar(&internal.FFmpegPath, "ffmpeg", "ffmpeg", "ffmpeg path")
	flag.StringVar(&hevc.VaapiDevice, "vaapi_device", "/dev/dri/renderD128", "ffmpeg vaapi_device")
	flag.Float64Var(&hevc.EncQualityPercent, "quality_percent", 0.6, "percentage bitrate quality according to source")
	flag.IntVar(&hevc.EncQualityPreset, "quality_preset", 20, "static quality preset (qp) passed to ffmpeg")
	flag.StringVar(&hevc.EncQualityType, "quality_type", hevc.EncQualityTypeAuto, "encoding quality type (auto/qp)")

	var file string
	var dir string
	var del bool
	var dryRun bool
	flag.StringVar(&file, "file", "", "source file path (cannot be combined with -dir)")
	flag.StringVar(&dir, "dir", "", "source files directory (cannot be combined with -file)")
	flag.BoolVar(&del, "del", false, "delete source files after successful conversion")
	flag.BoolVar(&dryRun, "dry", false, "run in dry mode = without actual conversion")

	flag.Parse()

	// validate
	if (file == "" && dir == "") || (file != "" && dir != "") {
		flag.PrintDefaults()
		return
	}

	// run
	if dryRun {
		internal.LogDebug("DRY RUN")
	}

	if file != "" {
		if err := hevc.Process(file, dryRun, del); err != nil {
			internal.LogError("could not process %s: %v", file, err)
		}
	}

	if dir != "" {
		internal.Walk(dir, func(path string, info os.FileInfo) {
			if err := hevc.Process(path, dryRun, del); err != nil {
				internal.LogError("could not process %s: %v", path, err)
			}
		})
	}
}
