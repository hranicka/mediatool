// Package dup reports duplicate audio tracks (same codec, bitrate, channel
// count and language tag) inside each file. Read-only — useful as a
// pre-flight audit before clean. Port of the bash dupfinder.sh (direct
// comparison instead of mediatool's MD5 hash).
package dup

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/walk"
)

// Config holds the dup subcommand settings.
type Config struct {
	Dir     string
	Ignores []string
	Verbose bool
}

type tool struct {
	r          run.Runner
	ffprobeBin string
	cfg        *Config
	ignores    ignore.List
	rep        *ui.Reporter

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
	cfg.Verbose = verbose
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediatool dup [flags] <directory>")
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

	ignores, warnings := ignore.Load(dir, "dup", cfg.Ignores)
	prof := ui.Detect(os.Stdout, os.Stderr)
	prof.Verbose = cfg.Verbose
	rep := ui.New(os.Stdout, prof)
	for _, w := range warnings {
		rep.Raw(ui.WarnTone, "warning: %s\n", w)
	}

	t := &tool{
		r:          run.Cmd{},
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		ignores:    ignores,
		rep:        rep,
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
	t.rep.Header([]ui.KV{
		ui.Pair("FFmpeg binary", ffmpegLabel()),
		ui.Pair("Target", t.cfg.Dir),
		ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())),
	})

	start := time.Now()
	files, err := walk.Media(t.cfg.Dir)
	if err != nil {
		return err
	}
	t.rep.Found(len(files))
	for i, path := range files {
		if t.ignores.Match(path) {
			t.rep.Skip(i+1, len(files), path, "ignored")
			continue
		}
		t.checkFile(i+1, len(files), path)
	}

	t.rep.Summary("Done.", "Files with duplicated audio tracks", "", ui.Outcome{
		Done:    t.filesWithDups,
		Elapsed: time.Since(start),
	})
	return nil
}

func ffmpegLabel() string {
	bin, _, err := run.Locate()
	if err != nil {
		return "ffmpeg"
	}
	return bin
}

func (t *tool) checkFile(i, n int, path string) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		t.rep.Skip(i, n, path, fmt.Sprintf("cannot probe: %v", err))
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
		t.rep.Raw(ui.WarnTone, "Duplicate audio tracks in %s: #%d #%s (codec/bitrate/channels/language: %s|%s|%s|%s)\n",
			path, first[k], strings.Join(ords, " #"), k.codec, k.bitRate, k.channels, k.lang)
	}
	if len(dupKeys) > 0 {
		t.filesWithDups++
	}
}
