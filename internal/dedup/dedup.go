// Package dedup reports groups of media files carrying the same content —
// different encodes, sizes, extensions or names of one movie or episode —
// by comparing perceptual hashes of frames sampled on a fixed interval.
// Matching tolerates shifted starts, cut scenes and framerate variants
// (windowed frame matching, the align_audio.py scheme). Read-only: .old
// backups never enter the walk and deletion stays manual.
package dedup

import (
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
	"github.com/hranicka/mediatool/internal/walk"
)

// Config holds the dedup subcommand settings.
type Config struct {
	Dir       string
	Interval  float64
	MinMatch  float64
	MaxDist   float64
	Tolerance float64
	Ignores   []string
	Verbose   bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	ignores    ignore.List
	rep        *ui.Reporter
}

// item is one scanned file with its cheap metadata; frame signatures load
// only for files with at least one duration-compatible candidate.
type item struct {
	path     string
	size     int64
	duration float64
	hasDur   bool
	width    int
	height   int
	codec    string
	hashes   [][vhash.HashLen]byte
	bg       int // median background distance, -1 when unknown
}

// Run executes the dedup subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("dedup", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.Float64Var(&cfg.Interval, "interval", 2, "frame sampling interval in seconds")
	fs.Float64Var(&cfg.MinMatch, "min-match", 0.8, "minimum fraction of reliably matched frames for two files to count as the same content")
	fs.Float64Var(&cfg.MaxDist, "max-dist", 0.15, "maximum normalized frame-hash distance (0-1) for a reliable frame match")
	fs.Float64Var(&cfg.Tolerance, "tolerance", 5, "duration tolerance in percent for candidate pairs (10 s floor)")
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cfg.Verbose = verbose
	if cfg.Interval <= 0 {
		return fmt.Errorf("-interval must be positive")
	}
	if cfg.MinMatch <= 0 || cfg.MinMatch > 1 {
		return fmt.Errorf("-min-match must be in (0, 1]")
	}
	if cfg.MaxDist <= 0 || cfg.MaxDist > 1 {
		return fmt.Errorf("-max-dist must be in (0, 1]")
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediatool dedup [flags] <directory>")
		return fmt.Errorf("dedup needs exactly one directory argument")
	}
	dir, err := cli.ResolveMediaTarget(fs.Arg(0), false)
	if err != nil {
		return err
	}
	cfg.Dir = dir

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	// a read-only report like dup: the shared ignore file and -i only
	ignores, warnings := ignore.Load(dir, "dedup", cfg.Ignores)
	prof := ui.Detect(os.Stdout, os.Stderr)
	prof.Verbose = cfg.Verbose
	rep := ui.New(os.Stdout, prof)
	for _, w := range warnings {
		rep.Raw(ui.WarnTone, "warning: %s\n", w)
	}

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		ignores:    ignores,
		rep:        rep,
	}
	return t.runAll()
}

func (t *tool) runAll() error {
	t.rep.Header([]ui.KV{
		ui.Pair("FFmpeg binary", t.ffmpegBin),
		ui.Pair("Frame interval", fmt.Sprintf("every %gs", t.cfg.Interval)),
		ui.Pair("Min match", fmt.Sprintf("%.0f%% of frames", t.cfg.MinMatch*100)),
		ui.Pair("Max distance", fmt.Sprintf("%.0f%% of hash bits", t.cfg.MaxDist*100)),
		ui.Pair("Duration tolerance", fmt.Sprintf("%g%% (10 s floor)", t.cfg.Tolerance)),
		ui.Pair("Target", t.cfg.Dir),
		ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())),
	})

	start := time.Now()
	files, err := walk.Media(t.cfg.Dir)
	if err != nil {
		return err
	}
	t.rep.Found(len(files))

	// phase 1: cheap metadata for every file
	items := t.scan(files)
	if n := countWithoutDuration(items); n > 0 {
		t.rep.Note(fmt.Sprintf("note: %d file(s) have no container duration and pair with everything - frame extraction may cover the whole library", n))
	}

	// phase 2: frame signatures only for the duration-compatible candidates
	candidates := candidateIndices(items, t.cfg.Tolerance)
	t.extract(items, candidates)

	// phase 3: pairwise comparison and the report
	matched := t.matchPairs(items)
	groups := unionGroups(len(items), matched)
	t.report(items, groups, time.Since(start))
	slog.Debug("done", "items", len(items), "candidates", len(candidates), "groups", len(groups))
	return nil
}

// scan probes every walked file and keeps those with a real video stream;
// the result drives everything else. Read-only, so failures just skip a
// file.
func (t *tool) scan(files []string) []item {
	items := make([]item, 0, len(files))
	for i, path := range files {
		if t.ignores.Match(path) {
			t.rep.Skip(i+1, len(files), path, "ignored")
			continue
		}
		t.rep.Phase("probing %d/%d %s", i+1, len(files), filepath.Base(path))
		it, err := t.probeItem(path)
		if err != nil {
			t.rep.Skip(i+1, len(files), path, err.Error())
			continue
		}
		items = append(items, it)
	}
	return items
}

func (t *tool) probeItem(path string) (item, error) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		return item{}, fmt.Errorf("cannot probe: %v", err)
	}
	videos := info.ByType()[probe.TypeVideo]
	var video *probe.Typed
	for i := range videos {
		if !media.IsCoverArt(videos[i].Stream) {
			video = &videos[i]
			break
		}
	}
	if video == nil {
		return item{}, fmt.Errorf("no real video stream")
	}
	it := item{
		path:   path,
		width:  video.Stream.Width,
		height: video.Stream.Height,
		codec:  video.Stream.CodecName,
	}
	if d, ok := info.Duration(); ok {
		it.duration, it.hasDur = d, true
	}
	if st, err := os.Stat(path); err == nil {
		it.size = st.Size()
	} else if s, ok := info.Size(); ok {
		it.size = s
	}
	return it, nil
}

// extract hashes the frame signatures of every candidate item; a failed
// extraction surfaces as a note and leaves the item out of the pairing.
func (t *tool) extract(items []item, candidates []int) {
	// background pairs must sit >=5 s apart on the timeline (ceil, so a
	// coarse interval never undershoots the measured calibration basis)
	minFrames := max(1, int(math.Ceil(5/t.cfg.Interval)))
	for k, idx := range candidates {
		it := &items[idx]
		t.rep.Phase("hashing %d/%d %s (decoding can take a while)", k+1, len(candidates), filepath.Base(it.path))
		hashes, err := vhash.Extract(t.r, t.ffmpegBin, it.path, t.cfg.Interval)
		if err != nil {
			t.rep.Note(fmt.Sprintf("note: %s: %v", it.path, err))
			continue
		}
		it.hashes = hashes
		it.bg = vhash.BackgroundDistance(hashes, minFrames, 200)
		slog.Debug("signatures", "path", it.path, "frames", len(hashes), "background", it.bg)
	}
}

// matchPairs compares every pair of signature-bearing items.
func (t *tool) matchPairs(items []item) [][2]int {
	// the window keeps the frame walk on track through shifted starts and
	// framerate drift: +-max(25 frames, 100 s) around the previous match
	window := int(math.Max(25, 100/t.cfg.Interval))
	maxBits := int(t.cfg.MaxDist * vhash.HashLen * 8)
	var matched [][2]int
	for i := range items {
		if len(items[i].hashes) == 0 {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			if len(items[j].hashes) == 0 {
				continue
			}
			if !compatibleDuration(items[i], items[j], t.cfg.Tolerance) {
				// transitive candidates may still be mutually incompatible
				// (A-B and B-C within tolerance, A-C not)
				continue
			}
			frac := matchFraction(items[i].hashes, items[j].hashes, window, maxBits, t.cfg.MinMatch)
			slog.Debug("pair match", "a", items[i].path, "b", items[j].path, "fraction", frac)
			if frac >= t.cfg.MinMatch {
				matched = append(matched, [2]int{i, j})
			}
		}
	}
	return matched
}

// report prints the duplicate groups — members sorted with the suggested
// keeper first, groups ordered by reclaimable space with a deterministic
// tie-break — plus the savings line and the summary.
func (t *tool) report(items []item, groups [][]int, elapsed time.Duration) {
	for _, members := range groups {
		sortMembers(items, members)
	}
	sort.Slice(groups, func(a, b int) bool {
		if sa, sb := groupSaving(items, groups[a]), groupSaving(items, groups[b]); sa != sb {
			return sa > sb
		}
		return items[groups[a][0]].path < items[groups[b][0]].path
	})
	var totalSaving int64
	for gi, members := range groups {
		var total int64
		for _, m := range members {
			total += items[m].size
		}
		saving := groupSaving(items, members)
		totalSaving += saving
		t.rep.Raw(ui.WarnTone, "Duplicate content group #%d: %d files, %s (%s reclaimable)\n",
			gi+1, len(members), media.HumanBytes(total), media.HumanBytes(saving))
		for k, m := range members {
			tag := "dup"
			if k == 0 {
				tag = "keep"
			}
			t.rep.Raw(ui.Neutral, "  %-5s%s (%dx%d %s, %s, %s)\n", tag, items[m].path,
				items[m].width, items[m].height, items[m].codec, clock(items[m].duration), media.HumanBytes(items[m].size))
		}
		if bg := groupBackground(items, members); bg >= 0 && bg < vhash.SelfSimilarBits {
			t.rep.Raw(ui.WarnTone, "  note: member frames are highly self-similar (background median %d of %d bits) - matches may be spurious, verify manually before deleting\n", bg, vhash.Side*vhash.Side)
		}
	}
	if len(groups) > 0 {
		t.rep.Raw(ui.Neutral, "\nPotential savings: %s in %d group(s); deletion is manual.\n", media.HumanBytes(totalSaving), len(groups))
	}
	t.rep.Summary("Done.", "Duplicate content groups", "", ui.Outcome{
		Done:    len(groups),
		Elapsed: elapsed,
	})
}

// groupSaving totals the members' sizes minus the keeper (member 0, which
// sortMembers placed first).
func groupSaving(items []item, members []int) int64 {
	var total int64
	for _, m := range members {
		total += items[m].size
	}
	return total - items[members[0]].size
}

// groupBackground returns the smallest known background median among the
// members, or -1 when none carries one.
func groupBackground(items []item, members []int) int {
	bg := -1
	for _, m := range members {
		if b := items[m].bg; b >= 0 && (bg == -1 || b < bg) {
			bg = b
		}
	}
	return bg
}

// sortMembers orders a group: the suggested keeper (highest resolution,
// then the larger file) first, the rest by size descending, with the path
// as the deterministic tie-break.
func sortMembers(items []item, members []int) {
	sort.Slice(members, func(a, b int) bool {
		x, y := items[members[a]], items[members[b]]
		if px, py := x.width*x.height, y.width*y.height; px != py {
			return px > py
		}
		if x.size != y.size {
			return x.size > y.size
		}
		return x.path < y.path
	})
}

// countWithoutDuration returns how many items ffprobe could not produce a
// container duration for; each of them pairs with everything, so the note
// explains why the run may hash the whole library.
func countWithoutDuration(items []item) int {
	n := 0
	for i := range items {
		if !items[i].hasDur {
			n++
		}
	}
	return n
}

// candidateIndices marks items with at least one duration-compatible peer;
// only those pay for frame extraction. Items without a usable duration
// pair with everything (the frame match decides).
func candidateIndices(items []item, tolPct float64) []int {
	compatible := make([]bool, len(items))
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if compatibleDuration(items[i], items[j], tolPct) {
				compatible[i], compatible[j] = true, true
			}
		}
	}
	var out []int
	for i, ok := range compatible {
		if ok {
			out = append(out, i)
		}
	}
	return out
}

func compatibleDuration(a, b item, tolPct float64) bool {
	if !a.hasDur || !b.hasDur {
		return true
	}
	return durationCompatible(a.duration, b.duration, tolPct)
}

// clock renders a media duration as h:mm:ss.
func clock(d float64) string {
	sec := int(math.Round(d))
	return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}
