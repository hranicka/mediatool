package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

const (
	typeAudio = "audio"
	codecDTS  = "dts"
	codecAC3  = "ac3"
)

var (
	commentaryRegExp = regexp.MustCompile(`(?i)(?:comment|director)`)
)

type tags struct {
	Language string `json:"language"`
	Title    string `json:"title"`
}

type stream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	BitRate   string `json:"bit_rate"`
	Tags      tags   `json:"tags"`
}

type ffprobe struct {
	Streams []stream `json:"streams"`
}

func log(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}

func main() {
	path := os.Args[1]
	log("opening file %s", path)

	var cmdOut bytes.Buffer
	var cmdErr bytes.Buffer
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path)
	cmd.Stdout = &cmdOut
	cmd.Stderr = &cmdErr

	if err := cmd.Run(); err != nil {
		log("cannot open file %v: %s", err, cmdErr.String())
		return
	}

	f := &ffprobe{}
	if err := json.Unmarshal(cmdOut.Bytes(), f); err != nil {
		log("cannot parse %s output: %v", "ffprobe", err)
		return
	}

	ac3Streams := make(map[string]stream)
	dtsStreams := make(map[string]stream)
	for _, s := range f.Streams {
		if s.CodecType != typeAudio {
			continue
		}

		switch s.CodecName {
		case codecAC3:
			ac3Streams[s.Tags.Language] = s
		case codecDTS:
			dtsStreams[s.Tags.Language] = s
		}
	}

	var dtsToConvert []stream
	for lang, dts := range dtsStreams {
		if ac3, ok := ac3Streams[lang]; ok {
			// exclude commentary and low bitrate tracks
			if commentaryRegExp.MatchString(ac3.Tags.Title) || ac3.BitRate != "640000" {
				log("> %s skipping low bitrate or commentary AC3 track", lang)
			} else {
				log("> %s DTS and AC3 streams found", lang)
				continue
			}
		}

		dtsToConvert = append(dtsToConvert, dts)
		log("> %s DTS without AC3 stream found", lang)
	}

	if len(dtsToConvert) == 0 {
		log("no conversion needed")
	}

	log("file finished")
	return
}
