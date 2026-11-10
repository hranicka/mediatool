// Package dup reports duplicate audio tracks (same codec, bitrate, channel
// count and language tag) inside each file. Read-only — useful as a
// pre-flight audit before clean. Port of the bash dupfinder.sh (direct
// comparison instead of mediatool's MD5 hash).
package dup

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/hranicka/media-tool/internal/cli"
	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/probe"
	"github.com/hranicka/media-tool/internal/run"
	"github.com/hranicka/media-tool/internal/walk"
)

// Config holds the dup subcommand settings.
type Config struct {
	Dir     string
	Ignores []string
}

type tool struct {
	r          run.Runner
	ffprobeBin string
	cfg        *Config
	ignores    ignore.List
	out        io.Writer

	filesWithDups int
}

// Run executes the dup subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("dup", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: media-tool dup [flags] <directory>")
		return fmt.Errorf("dup needs exactly one directory argument")
	}
	dir, err := cli.ResolveMediaTarget(fs.Arg(0), false)
	if err != nil {
		return err
	}
	cfg.Dir = dir

	_, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	t := &tool{
		r:          run.Cmd{},
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		ignores:    ignore.Load(dir, "dup", cfg.Ignores),
		out:        os.Stdout,
	}
	return t.runAll()
}

// trackKey identifies an audio track by its deciding properties.
type trackKey struct {
	codec    string
	bitRate  string
	channels string
	lang     string
}

func (t *tool) runAll() error {
	fmt.Fprintf(t.out, "FFmpeg binary : %s\n", ffmpegLabel())
	fmt.Fprintf(t.out, "Target        : %s\n", t.cfg.Dir)
	fmt.Fprintf(t.out, "Ignore entries: %d\n", t.ignores.Len())
	fmt.Fprintln(t.out, "==========================================")

	files, err := walk.Media(t.cfg.Dir)
	if err != nil {
		return err
	}
	for _, path := range files {
		if t.ignores.Match(path) {
			fmt.Fprintf(t.out, "Skipping (ignored): %s\n", path)
			continue
		}
		t.checkFile(path)
	}

	fmt.Fprintln(t.out, "==========================================")
	fmt.Fprintf(t.out, "Done. Files with duplicated audio tracks: %d.\n", t.filesWithDups)
	return nil
}

func ffmpegLabel() string {
	bin, _, err := run.Locate()
	if err != nil {
		return "ffmpeg"
	}
	return bin
}

func (t *tool) checkFile(path string) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		fmt.Fprintf(t.out, "Skipping (cannot probe: %v): %s\n", err, path)
		return
	}

	first := map[trackKey]int{}
	var dupKeys []trackKey
	dups := map[trackKey][]int{}
	for _, ta := range info.ByType()[probe.TypeAudio] {
		k := trackKey{
			codec:    ta.Stream.CodecName,
			bitRate:  ta.Stream.BitRate,
			channels: strconv.Itoa(ta.Stream.Channels),
			lang:     ta.Stream.Tags.Language,
		}
		if _, ok := first[k]; ok {
			if _, seen := dups[k]; !seen {
				dupKeys = append(dupKeys, k)
			}
			dups[k] = append(dups[k], ta.Ordinal)
			continue
		}
		first[k] = ta.Ordinal
	}

	sort.Slice(dupKeys, func(i, j int) bool {
		return dupKeys[i].codec+dupKeys[i].lang < dupKeys[j].codec+dupKeys[j].lang
	})
	for _, k := range dupKeys {
		var ords []string
		for _, o := range dups[k] {
			ords = append(ords, strconv.Itoa(o))
		}
		fmt.Fprintf(t.out, "Duplicate audio tracks in %s: #%d #%s (codec/bitrate/channels/language: %s|%s|%s|%s)\n",
			path, first[k], strings.Join(ords, " #"), k.codec, k.bitRate, k.channels, k.lang)
	}
	if len(dupKeys) > 0 {
		t.filesWithDups++
	}
}
