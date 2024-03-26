package main

import (
	"crypto/md5"
	"encoding/hex"
	"flag"
	"log/slog"
	"os"
	"strconv"

	"github.com/hranicka/mediatool/internal"
)

func main() {
	// parse cli args
	verbose := flag.Bool("v", false, "verbose/debug output")

	var dir string
	flag.StringVar(&dir, "dir", "", "source files directory")

	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// validate
	if dir == "" {
		flag.PrintDefaults()
		return
	}

	internal.Walk(dir, func(path string, info os.FileInfo) {
		slog.Debug("opening file", "path", path)

		f, err := internal.Probe(path)
		if err != nil {
			slog.Error("cannot probe file", "path", path, "err", err)
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
			slog.Info("possibly duplicated tracks", "path", path, "streams", dups)
		}

		slog.Debug("file finished", "path", path)
	})
}
