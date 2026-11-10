// Command mediatool compacts and manages a personal media library
// (MKV/MP4) by re-encoding video to HEVC, appending compatibility audio
// tracks, removing unwanted streams and reporting duplicates.
package main

import (
	"fmt"
	"os"

	"github.com/hranicka/mediatool/internal/ac3"
	"github.com/hranicka/mediatool/internal/clean"
	"github.com/hranicka/mediatool/internal/convert"
	"github.com/hranicka/mediatool/internal/dup"
	"github.com/hranicka/mediatool/internal/enhance"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usageText = `MediaTool 2.0 — media library compaction and management.

Usage: mediatool <command> [flags]

Commands:
  convert   re-encode H.264 video tracks to HEVC (nvenc/qsv/vaapi/x265)
  clean     remove audio/subtitle tracks of non-whitelisted languages
  ac3       append AC3 compatibility tracks for DTS/TrueHD/FLAC audio
  dup       report duplicate audio tracks (read-only)
  enhance   AI-upscale with Real-ESRGAN and re-encode to HEVC 1080p
  version   print the version

Library-touching commands work on a directory (or a single file where noted),
honor the dot ignore files in the target directory, keep .old backups and
support a dry run. Run "mediatool <command> -h" for details.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "convert":
		err = convert.Run(os.Args[2:])
	case "clean":
		err = clean.Run(os.Args[2:])
	case "ac3":
		err = ac3.Run(os.Args[2:])
	case "dup":
		err = dup.Run(os.Args[2:])
	case "enhance":
		err = enhance.Run(os.Args[2:])
	case "version":
		fmt.Println("mediatool " + version)
		return
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
