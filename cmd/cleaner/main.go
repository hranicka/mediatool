package main

import (
	"flag"
	"github.com/hranicka/mediatool/internal/cleaner"
	"log/slog"
	"os"

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
	flag.StringVar(&file, "file", "", "source file path (cannot be combined with -dir)")
	flag.StringVar(&dir, "dir", "", "source files directory (cannot be combined with -file)")
	flag.BoolVar(&del, "del", false, "delete source files after successful conversion")
	flag.BoolVar(&dryRun, "dry", false, "run in dry mode = without actual conversion")

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
		if err := cleaner.Process(file, dryRun, del); err != nil {
			slog.Error("could not process", "file", file, "error", err)
		}
	}

	if dir != "" {
		internal.Walk(dir, func(path string, info os.FileInfo) {
			if err := cleaner.Process(path, dryRun, del); err != nil {
				slog.Error("could not process", "file", path, "error", err)
			}
		})
	}
}
