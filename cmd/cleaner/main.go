package main

import (
	"flag"
	"github.com/hranicka/mediatool/internal/cleaner"
	"os"

	"github.com/hranicka/mediatool/internal"
)

func main() {
	// parse cli args
	flag.BoolVar(&internal.Verbose, "v", false, "verbose/debug output")
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
		if err := cleaner.Process(file, dryRun, del); err != nil {
			internal.LogError("could not process %s: %v", file, err)
		}
	}

	if dir != "" {
		internal.Walk(dir, func(path string, info os.FileInfo) {
			if err := cleaner.Process(path, dryRun, del); err != nil {
				internal.LogError("could not process %s: %v", path, err)
			}
		})
	}
}
