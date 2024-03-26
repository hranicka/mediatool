package main

import (
	"flag"
	"github.com/hranicka/mediatool/internal/ac3"
	"log/slog"
	"os"
	"strings"

	"github.com/hranicka/mediatool/internal"
)

func main() {
	// parse cli args
	verbose := flag.Bool("v", false, "verbose/debug output")
	flag.StringVar(&internal.FFmpegPath, "ffmpeg", "ffmpeg", "ffmpeg path")

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

	var minBitRate int
	var lang string
	flag.IntVar(&minBitRate, "minbr", 448000, "minimal bitrate of track to be considered as valid/already converted")
	flag.StringVar(&lang, "lang", "", "yet not converted language to trigger conversion of the whole file")

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
		slog.Info("DRY RUN")
	}

	if file != "" {
		if err := ac3.Process(file, lang, minBitRate, dryRun, del); err != nil {
			slog.Error("could not process", "file", file, "error", err)
		}
	}

	if dir != "" {
		ignores := internal.FileIgnoresWhenExist(dir + "/.ac3converter-ignore")
		if ignore != "" {
			ignores = append(ignores, strings.Split(ignore, ",")...)
		}

		internal.Walk(dir, ignores, func(path string, info os.FileInfo) {
			if err := ac3.Process(path, lang, minBitRate, dryRun, del); err != nil {
				slog.Error("could not process", "file", path, "error", err)
			}
		})
	}
}
