package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hranicka/mediatool/internal"
)

// Version generated during build process.
var version string

func main() {
	// parse cli args
	v := flag.Bool("version", false, "prints application version")
	flag.BoolVar(&internal.Verbose, "v", false, "verbose/debug output")

	var file string
	var dir string
	var del bool
	var dryRun bool
	flag.StringVar(&file, "file", "", "source file path (cannot be combined with -dir)")
	flag.StringVar(&dir, "dir", "", "source files directory (cannot be combined with -file)")
	flag.BoolVar(&del, "del", false, "delete source files after successful conversion")
	flag.BoolVar(&dryRun, "dry", false, "run in dry mode = without actual conversion")

	var minBitRate int
	var lang string
	flag.IntVar(&minBitRate, "minbr", 448000, "minimal bitrate of track to be considered as valid/already converted")
	flag.StringVar(&lang, "lang", "", "yet not converted language to trigger conversion of the whole file")

	flag.Parse()

	// Print version
	if *v {
		fmt.Println(version)
		return
	}

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
		if err := internal.Process(file, lang, minBitRate, dryRun, del); err != nil {
			internal.LogError("could not process %s: %v", file, err)
		}
	}

	if dir != "" {
		internal.Walk(dir, func(path string, info os.FileInfo) {
			if err := internal.Process(path, lang, minBitRate, dryRun, del); err != nil {
				internal.LogError("could not process %s: %v", path, err)
			}
		})
	}
}
