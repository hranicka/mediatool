package main

import (
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/hranicka/mediatool/internal"
)

// Version generated during build process.
var version string

func main() {
	// parse cli args
	v := flag.Bool("version", false, "prints application version")
	flag.BoolVar(&internal.Verbose, "v", false, "verbose/debug output")

	var dir string
	flag.StringVar(&dir, "dir", "", "source files directory")

	flag.Parse()

	// Print version
	if *v {
		fmt.Println(version)
		return
	}

	// validate
	if dir == "" {
		flag.PrintDefaults()
		return
	}

	internal.Walk(dir, func(path string, info os.FileInfo) {
		internal.LogDebug("opening file %s", path)

		f, err := internal.Probe(path)
		if err != nil {
			internal.LogError("cannot get file info of %s: %v", path, err)
			return
		}

		data := make(map[string]internal.Stream)
		dups := make(map[string][]internal.Stream)
		for _, s := range f.Streams {
			if s.CodecType != internal.TypeAudio {
				continue
			}

			m := md5.New()
			m.Write([]byte(s.CodecType))
			m.Write([]byte(s.CodecName))
			m.Write([]byte(s.BitRate))
			m.Write([]byte(strconv.Itoa(s.Channels)))
			m.Write([]byte(s.Tags.Language))
			hash := hex.EncodeToString(m.Sum(nil))

			if d, ok := data[hash]; ok {
				if len(dups[hash]) == 0 {
					dups[hash] = append(dups[hash], d)
				}
				dups[hash] = append(dups[hash], s)
				continue
			}
			data[hash] = s
		}

		if len(dups) > 0 {
			internal.LogInfo("possibly duplicated tracks in %s", path)
			for _, s := range dups {
				internal.LogInfo("> %+v", s)

			}
			internal.LogInfo("\n")
		}

		internal.LogDebug("file finished")
	})
}
