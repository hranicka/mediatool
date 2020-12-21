package main

import (
	"fmt"
	"os"

	"github.com/pixelbender/go-matroska/matroska"
)

const (
	typeAudio = 2
	codecDTS  = "A_DTS"
	codecAC3  = "A_AC3"
)

type Track struct {
	Type  int
	Lang  string
	Codec string
	Name  string
}

func log(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}

func main() {
	path := os.Args[1]
	log("opening file %s", path)

	doc, err := matroska.Decode(path)
	if err != nil {
		log("cannot open file %s: %w", path, err)
		return
	}

	ac3Tracks := make(map[string]*Track)
	dtsTracks := make(map[string]*Track)
	for _, t := range doc.Segment.Tracks {
		for _, te := range t.Entries {
			track := &Track{
				Type:  int(te.Type),
				Codec: te.CodecID,
				Lang:  te.Language,
				Name:  te.Name,
			}

			if track.Type != typeAudio {
				continue
			}

			switch track.Codec {
			case codecAC3:
				ac3Tracks[track.Lang] = track
			case codecDTS:
				dtsTracks[track.Lang] = track
			}
		}
	}

	var dtsToConvert []*Track
	for lang, dts := range dtsTracks {
		if _, ok := ac3Tracks[lang]; ok {
			log("> %s DTS and AC3 tracks found", lang)
			continue
		}

		dtsToConvert = append(dtsToConvert, dts)
		log("> %s DTS without AC3 found", lang)
	}
	if len(dtsToConvert) == 0 {
		log("no conversion needed")
	}
	log("file finished\n")
}
