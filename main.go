package main

import (
	"bytes"
	"encoding/json"
	"flag"
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
	// parse cli args
	var src string
	var dryRun bool
	flag.StringVar(&src, "file", "", "source file path")
	flag.BoolVar(&dryRun, "dry", false, "run in dry mode = without actual conversion")
	flag.Parse()

	if src == "" {
		flag.PrintDefaults()
		return
	}
	if dryRun {
		log("DRY RUN")
	}

	// read file streams
	log("opening file %s", src)
	f, err := probe(src)
	if err != nil {
		log("cannot get file info: %v", err)
		return
	}

	valid := make(map[string]stream)
	bad := make(map[string]stream)
	for _, s := range f.Streams {
		if s.CodecType != typeAudio {
			continue
		}

		switch s.CodecName {
		case codecAC3:
			valid[s.Tags.Language] = s
		case codecDTS:
			bad[s.Tags.Language] = s
		}
	}

	var toConvert []stream
	for lang, bs := range bad {
		if vs, ok := valid[lang]; ok {
			// exclude commentary and low bitrate tracks
			if commentaryRegExp.MatchString(vs.Tags.Title) || vs.BitRate != "640000" {
				log("> %s: skipping low bitrate or commentary track", lang)
			} else {
				log("> %s: already converted stream found", lang)
				continue
			}
		}

		toConvert = append(toConvert, bs)
		log("> %s: stream for conversion found", lang)
	}

	// convert if needed
	if len(toConvert) == 0 {
		log("no conversion needed")
	} else {
		log("converting %d DTS track(s)", len(toConvert))

		if !dryRun {
			dst := src
			src += ".old"
			if err := os.Rename(dst, src); err != nil {
				log("cannot rename original file: %v", err)
				return
			}

			if err := convert(src, dst, toConvert); err != nil {
				log("cannot convert file: %v: %s", err)
				if err := os.Rename(src, dst); err != nil {
					log("cannot rename original file back: %v", err)
				}
				return
			}
		}
	}

	log("file finished\n")
	return
}

func probe(src string) (*ffprobe, error) {
	out, err := runCmd("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", src)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %v", err)
	}

	f := &ffprobe{}
	if err := json.Unmarshal(out, f); err != nil {
		return nil, fmt.Errorf("cannot parse %s output: %v", "ffprobe", err)
	}

	return f, nil
}

func convert(src string, dst string, streams []stream) error {
	args := []string{"-i", src, "-map", "0:v"}
	for _, s := range streams {
		args = append(args, "-map", fmt.Sprintf("0:%d", s.Index))
	}
	args = append(args, "-map", "0:a", "-map", "0:s", "-c:v", "copy", "-c:a", "copy", "-c:s", "copy")
	for _, s := range streams {
		args = append(args, fmt.Sprintf("-c:%d", s.Index), "ac3", fmt.Sprintf("-b:%d", s.Index), "640k")
	}
	args = append(args, dst)

	_, err := runCmd("ffmpeg", args...)
	return err
}

func runCmd(name string, arg ...string) ([]byte, error) {
	var cmdOut bytes.Buffer
	var cmdErr bytes.Buffer
	cmd := exec.Command(name, arg...)
	cmd.Stdout = &cmdOut
	cmd.Stderr = &cmdErr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, cmdErr.String())
	}
	return cmdOut.Bytes(), nil
}
