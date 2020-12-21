package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
)

const (
	typeAudio = "audio"
	codecDTS  = "dts"
	codecAC3  = "ac3"
)

var (
	commentRegExp = regexp.MustCompile(`(?i)(?:comment|director)`)
	filePattern   = regexp.MustCompile(`(?i)\.mkv$`)

	dryRun  = false
	verbose = false
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

func logDebug(format string, a ...interface{}) {
	if verbose {
		fmt.Printf("debug: "+format+"\n", a...)
	}
}

func logInfo(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}

func logError(format string, a ...interface{}) {
	fmt.Printf("err: "+format+"\n", a...)
}

func main() {
	// parse cli args
	flag.BoolVar(&dryRun, "dry", false, "run in dry mode = without actual conversion")
	flag.BoolVar(&verbose, "v", false, "verbose/debug output")

	var file string
	var dir string
	flag.StringVar(&file, "file", "", "source file path (cannot be combined with -dir)")
	flag.StringVar(&dir, "dir", "", "source files directory (cannot be combined with -file)")

	var minBitRate int
	var minFileSize int64
	var lang string
	flag.IntVar(&minBitRate, "minbr", 480000, "minimal bitrate of track to be considered as valid/already converted")
	flag.Int64Var(&minFileSize, "minfs", 0, "minimal file size to be processed (only with -dir)")
	flag.StringVar(&lang, "lang", "", "language of track to be processed; leave empty for all languages")

	flag.Parse()

	// validate
	if (file == "" && dir == "") || (file != "" && dir != "") {
		flag.PrintDefaults()
		return
	}

	// run
	if dryRun {
		logInfo("DRY RUN")
	}

	if file != "" {
		if err := process(file, minBitRate, lang); err != nil {
			logError("could not process %s: %v", file, err)
		}
	}

	if dir != "" {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if !filePattern.MatchString(path) {
				logDebug("skipping unmatched file name %s", path)
				return nil
			}

			// TODO Bitrate would be much more suitable than file size
			if info.Size() < minFileSize {
				logDebug("skipping small file %s", path)
				return nil
			}

			if err := process(path, minBitRate, lang); err != nil {
				logError("could not process %s: %v", file, err)
			}
			return nil
		})
	}
}

func process(src string, minBitRate int, lang string) error {
	// read file streams
	logInfo("opening file %s", src)
	f, err := probe(src)
	if err != nil {
		return fmt.Errorf("cannot get file info: %v", err)
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
	for l, bs := range bad {
		if lang != "" && l != lang {
			logInfo("> %s: not required language, skipping", l)
			continue
		}

		if vs, ok := valid[l]; ok {
			// exclude commentary and low bitrate tracks
			if commentRegExp.MatchString(vs.Tags.Title) || parseInt(vs.BitRate) < minBitRate {
				logInfo("> %s: low bitrate or commentary stream, skipping", l)
			} else {
				logInfo("> %s: already converted stream, skipping", l)
				continue
			}
		}

		toConvert = append(toConvert, bs)
		logInfo("> %s: stream for conversion found", l)
	}

	// convert if needed
	if len(toConvert) == 0 {
		logInfo("no conversion needed")
	} else {
		logInfo("converting %d DTS track(s)", len(toConvert))
		logDebug("%+v", toConvert)

		if !dryRun {
			dst := src + ".tmp.mkv" // TODO Validate original extension
			if err := convert(src, dst, toConvert); err != nil {
				if err := os.Remove(dst); err != nil {
					logError("cannot delete temporary file: %v", err)
				}
				return fmt.Errorf("cannot convert file: %v", err)
			}

			old := src + ".old"
			if err := os.Rename(src, old); err != nil {
				return fmt.Errorf("cannot rename original file: %v", err)
			}
			if err := os.Rename(dst, src); err != nil {
				return fmt.Errorf("cannot rename converted file: %v", err)
			}
		}
	}

	logInfo("file finished\n")
	return nil
}

func parseInt(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

func probe(src string) (*ffprobe, error) {
	out, err := runCmd("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", src)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %v", err)
	}
	logDebug(string(out))

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
	args = append(args, "-max_muxing_queue_size", "8096", dst)

	_, err := runCmd("ffmpeg", args...)
	return err
}

func runCmd(name string, arg ...string) ([]byte, error) {
	var cmdOut bytes.Buffer
	var cmdErr bytes.Buffer
	cmd := exec.Command(name, arg...)
	cmd.Stdout = &cmdOut
	cmd.Stderr = &cmdErr

	logDebug(cmd.String())
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, cmdErr.String())
	}
	return cmdOut.Bytes(), nil
}
