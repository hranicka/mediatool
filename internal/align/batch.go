package align

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/backup"
	"github.com/hranicka/mediatool/internal/gates"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/walk"
)

// runBatch pairs the reference and dub files, then aligns every couple in
// place: the reference is replaced by the aligned file and both inputs
// keep .old backups unless -no-backup is set.
func (t *tool) runBatch() error {
	backupNote := "keep .old for both inputs"
	if t.cfg.NoBackup {
		backupNote = "none (-no-backup)"
	}
	dubSide := t.cfg.DubDir
	if t.cfg.Dub != "" {
		dubSide = t.cfg.Dub
	}
	if t.cfg.RefDir != "" && t.cfg.RefDir == t.cfg.DubDir {
		dubSide = fmt.Sprintf("same directory (suffix %q)", t.cfg.DubSuffix)
	}
	refSide := t.cfg.RefDir
	if t.cfg.Ref != "" {
		refSide = t.cfg.Ref
	}
	kvs := []ui.KV{}
	if t.cfg.Ref != "" {
		kvs = append(kvs, ui.Pair("Reference", refSide))
	} else {
		kvs = append(kvs, ui.Pair("Reference dir", refSide))
	}
	if t.cfg.Dub != "" {
		kvs = append(kvs, ui.Pair("Dubbed", dubSide))
	} else {
		kvs = append(kvs, ui.Pair("Dubbed dir", dubSide))
	}
	kvs = append(kvs,
		ui.Pair("Pairing", t.pairingLabel()),
		ui.Pair("Backups", backupNote),
		ui.Pair("Frame cache", t.cacheLabel()),
		ui.Pair("Ignore entries", fmt.Sprint(t.ignores.Len())))
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()
	refs, dubs, err := t.walkInputs()
	if err != nil {
		return err
	}
	t.rep.Found(len(refs) + len(dubs))

	var plan pairing
	switch {
	case t.cfg.Mixed && t.cfg.Ref != "" && len(refs) == 0:
		plan = pairing{suggestions: map[string]suggestion{}}
	case t.cfg.Mixed && t.cfg.Dub != "" && len(dubs) == 0:
		plan = pairing{suggestions: map[string]suggestion{}}
	case t.cfg.Mixed && len(t.cfg.Pairs) > 0:
		if t.cfg.Ref != "" {
			plan, err = pairFiles([]string{t.cfg.Ref}, dubs, t.cfg.Pairs, "")
		} else {
			plan, err = pairFiles(refs, []string{t.cfg.Dub}, t.cfg.Pairs, "")
		}
	case t.cfg.Mixed && t.cfg.Ref != "":
		plan = pairOne(t.cfg.Ref, dubs, true)
	case t.cfg.Mixed:
		plan = pairOne(t.cfg.Dub, refs, false)
	default:
		plan, err = pairFiles(refs, dubs, t.cfg.Pairs, t.cfg.DubSuffix)
	}
	if err != nil {
		return err
	}
	if !t.cfg.NoContentPair && len(refs) > 0 && len(dubs) > 0 && pairingHasWeak(plan, t) {
		plan = t.contentRepair(plan, refs, dubs)
	}
	t.printPairing(plan)

	if t.cfg.PairOnly {
		t.rep.Summary("Done.", "Paired", "skipped", ui.Outcome{
			Done:    len(plan.pairs),
			Skipped: t.skipped,
			Elapsed: time.Since(start),
		})
		return nil
	}

	for i, p := range plan.pairs {
		t.processPair(i+1, len(plan.pairs), p)
	}

	doneLabel := "Aligned"
	if t.cfg.DryRun {
		doneLabel = "Would align"
	}
	t.rep.Summary("Done.", doneLabel, "skipped", ui.Outcome{
		Done:     t.aligned,
		Skipped:  t.skipped,
		Failed:   t.failed,
		BytesIn:  t.bytesIn,
		BytesOut: t.bytesOut,
		Failures: t.failures,
		Elapsed:  time.Since(start),
	})
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

// walkInputs discovers the reference and dub files, honoring the ignore
// rules. Given files are used directly; a single directory is split by
// the dub suffix; otherwise both directories are walked.
func (t *tool) walkInputs() (refs, dubs []string, err error) {
	if t.cfg.Ref != "" {
		refs = t.filterSingle(t.cfg.Ref)
	}
	if t.cfg.Dub != "" {
		dubs = t.filterSingle(t.cfg.Dub)
	}
	if t.cfg.RefDir != "" && t.cfg.RefDir == t.cfg.DubDir && t.cfg.Ref == "" && t.cfg.Dub == "" {
		files, err := walk.Media(t.cfg.RefDir)
		if err != nil {
			return nil, nil, err
		}
		for i, path := range files {
			if t.ignores.Match(path) {
				t.rep.Skip(i+1, len(files), path, "ignored")
				t.skipped++
				continue
			}
			if hasDubSuffix(path, t.cfg.DubSuffix) {
				dubs = append(dubs, path)
			} else {
				refs = append(refs, path)
			}
		}
		return refs, dubs, nil
	}
	if t.cfg.Ref == "" && t.cfg.RefDir != "" {
		files, err := walk.Media(t.cfg.RefDir)
		if err != nil {
			return nil, nil, err
		}
		refs = t.filterIgnored(files)
	}
	if t.cfg.Dub == "" && t.cfg.DubDir != "" {
		files, err := walk.Media(t.cfg.DubDir)
		if err != nil {
			return nil, nil, err
		}
		dubs = t.filterIgnored(files)
	}
	return refs, dubs, nil
}

// filterSingle applies the ignore rules to an explicitly given file.
func (t *tool) filterSingle(path string) []string {
	if t.ignores.Match(path) {
		t.rep.Skip(1, 1, path, "ignored")
		t.skipped++
		return nil
	}
	return []string{path}
}

func (t *tool) filterIgnored(files []string) []string {
	out := make([]string, 0, len(files))
	for i, path := range files {
		if t.ignores.Match(path) {
			t.rep.Skip(i+1, len(files), path, "ignored")
			t.skipped++
			continue
		}
		out = append(out, path)
	}
	return out
}

// printPairing renders the pairing plan before any decoding starts.
func (t *tool) printPairing(plan pairing) {
	for _, note := range plan.notes {
		t.rep.Raw(ui.WarnTone, "  note: %s\n", note)
	}
	for _, p := range plan.pairs {
		t.rep.Raw(ui.GoodTone, "  pair: %s <- %s  [%s]\n",
			t.shortPath(t.refBase(), p.ref), t.shortPath(t.dubBase(), p.dub), p.how)
	}
	for _, ref := range plan.unmatchedRefs {
		t.rep.Raw(ui.WarnTone, "  skip: %s - no counterpart\n", t.shortPath(t.refBase(), ref))
		if s, ok := plan.suggestions[ref]; ok {
			t.rep.Raw(ui.DimTone, "        closest dub: %s (%s, score %.2f)\n",
				t.shortPath(t.dubBase(), s.path), s.kind, s.score)
		}
		t.skipped++
	}
	for _, dub := range plan.unmatchedDubs {
		t.rep.Raw(ui.DimTone, "  unused dub: %s\n", t.shortPath(t.dubBase(), dub))
		t.skipped++
	}
	if len(plan.pairs) == 0 {
		t.rep.Raw(ui.WarnTone, "No reference/dub pairs found.\n")
	}
}

func (t *tool) shortPath(base, path string) string {
	if base == "" {
		return path
	}
	if rel, err := filepath.Rel(base, path); err == nil {
		return rel
	}
	return path
}

// refBase is the directory used to shorten reference paths in reports.
func (t *tool) refBase() string {
	if t.cfg.RefDir != "" {
		return t.cfg.RefDir
	}
	if t.cfg.Ref != "" {
		return filepath.Dir(t.cfg.Ref)
	}
	return ""
}

// dubBase is the directory used to shorten dub paths in reports.
func (t *tool) dubBase() string {
	if t.cfg.DubDir != "" {
		return t.cfg.DubDir
	}
	if t.cfg.Dub != "" {
		return filepath.Dir(t.cfg.Dub)
	}
	return ""
}

// processPair aligns one couple dry-run or in place. Failures stay local
// to the file and do not abort the batch.
func (t *tool) processPair(i, n int, p pair) {
	if p.ref == p.dub {
		t.rep.Skip(i, n, p.ref, "reference and dub are the same file")
		t.skipped++
		return
	}
	in, err := t.probeInputs(p.ref, p.dub)
	if err != nil {
		t.fail(p.ref, err.Error())
		return
	}
	if hasDubbedTrack(in.refInfo) {
		t.rep.Skip(i, n, p.ref, "already has a dubbed track")
		t.skipped++
		return
	}
	if !in.dubLangMatched {
		t.rep.Note(fmt.Sprintf("note: %s has no '%s' audio track - using audio #%d", p.dub, t.cfg.LangDub, in.dubAudio.Ordinal))
	}
	dest, outFmt := outputTarget(p.ref, in.refInfo)
	if dest != p.ref {
		if _, err := os.Stat(dest); err == nil {
			t.rep.Skip(i, n, p.ref, fmt.Sprintf("output %s already exists", dest))
			t.skipped++
			return
		}
	}
	if !t.cfg.NoBackup {
		for _, backupPath := range []string{p.ref + ".old", p.dub + ".old"} {
			if _, err := os.Stat(backupPath); err == nil {
				t.rep.Skip(i, n, p.ref, fmt.Sprintf("backup %s already exists", backupPath))
				t.skipped++
				return
			}
		}
	}

	fileStart := time.Now()
	verb := "Aligning"
	if t.cfg.DryRun {
		verb = "Analysing"
	}
	t.rep.Start(verb, i, n, p.ref, media.HumanBytes(in.refSize), time.Duration(in.refDur*float64(time.Second)),
		fmt.Sprintf("dubbed: %s", p.dub),
		fmt.Sprintf("pairing: %s", p.how),
		fmt.Sprintf("dub audio: %s", t.dubAudioLabel(in)))
	t.rep.Detail("output: %s", dest)

	fit, err := t.buildFit(in)
	if err != nil {
		t.fail(p.ref, err.Error())
		return
	}
	t.printFit(fit, in)

	if t.cfg.DryRun {
		action := fmt.Sprintf("replace %s with the aligned file", p.ref)
		details := []string{fmt.Sprintf("output: %s", dest)}
		if t.cfg.NoBackup {
			details = append(details, "reference replaced in place, dub removed (no backups)")
		} else {
			details = append(details, fmt.Sprintf("backups: %s, %s", p.ref+".old", p.dub+".old"))
		}
		t.rep.Would("Would align", i, n, p.ref,
			fmt.Sprintf("speed %.4f, %d segment(s), %.0f%% of the shared span matched",
				fit.Speed, len(fit.Segments), fit.InlierFrac*100),
			append([]string{action}, details...)...)
		t.aligned++
		return
	}

	tmp := fmt.Sprintf("%s.%d.tmp", p.ref, os.Getpid())
	t.guard.Add(tmp)
	defer func() {
		t.guard.Remove(tmp)
		if _, err := os.Stat(tmp); err == nil {
			os.Remove(tmp)
		}
	}()
	if err := t.muxAligned(in, fit, tmp, outFmt); err != nil {
		t.fail(p.ref, err.Error())
		return
	}
	if err := t.gateOutput(tmp, in); err != nil {
		t.fail(p.ref, err.Error())
		return
	}

	var mod time.Time
	if st, err := os.Stat(p.ref); err == nil {
		mod = st.ModTime()
	}
	var backupPath string
	if t.cfg.NoBackup {
		if err := backup.ReplacePair(p.ref, p.dub, tmp, dest); err != nil {
			t.fail(p.ref, err.Error())
			return
		}
	} else {
		backupPath, err = backup.SwapPair(p.ref, p.dub, tmp, dest)
		if err != nil {
			t.fail(p.ref, err.Error())
			return
		}
	}
	if !mod.IsZero() {
		os.Chtimes(dest, mod, mod)
	}
	if t.cfg.NoBackup {
		// The originals are gone: drop their cached signatures too.
		for _, path := range []string{p.ref, p.dub} {
			if err := t.cache.Remove(path, t.cfg.Interval); err != nil {
				slog.Warn("cannot remove the frame cache entry", "path", path, "err", err)
			} else {
				slog.Debug("frame cache entry removed", "path", path)
			}
		}
	}

	newSize := int64(0)
	if st, err := os.Stat(dest); err == nil {
		newSize = st.Size()
	}
	t.bytesIn += in.refSize
	t.bytesOut += newSize
	if t.cfg.NoBackup {
		t.rep.SuccessNoBackup("Aligned", dest, in.refSize, newSize, time.Since(fileStart))
		t.rep.Detail("dub removed: %s", p.dub)
	} else {
		t.rep.Success("Aligned", dest, in.refSize, newSize, backupPath, time.Since(fileStart))
		t.rep.Detail("dub backup: %s", p.dub+".old")
	}
	t.aligned++
}

// fail records a per-file failure and keeps the batch running.
func (t *tool) fail(path, msg string) {
	t.rep.Fail(msg, "")
	t.failures = append(t.failures, ui.Failure{Path: path, Msg: msg})
	t.failed++
}

// hasDubbedTrack reports whether the reference already carries a track
// produced by a previous align run. MP4 stores the marker as
// handler_name, Matroska as title.
func hasDubbedTrack(info *probe.Info) bool {
	for _, a := range info.ByType()[probe.TypeAudio] {
		if isDubbedTag(a.Stream.Tags) {
			return true
		}
	}
	return false
}

func isDubbedTag(tags probe.Tags) bool {
	marker := "dubbed (resampled)"
	return strings.Contains(strings.ToLower(tags.Title), marker) ||
		strings.Contains(strings.ToLower(tags.HandlerName), marker)
}

// outputTarget keeps the reference container when it can carry the new
// track and falls back to MKV when it cannot (subtitles, attachments,
// unsupported streams).
func outputTarget(ref string, info *probe.Info) (path, format string) {
	switch strings.ToLower(filepath.Ext(ref)) {
	case ".mkv":
		return ref, "matroska"
	case ".mp4":
		if mp4Safe(info) {
			return ref, "mp4"
		}
		return withExt(ref, ".mkv"), "matroska"
	default:
		return withExt(ref, ".mkv"), "matroska"
	}
}

// mp4Safe reports whether every reference stream can be copied into MP4
// without conversion.
func mp4Safe(info *probe.Info) bool {
	for _, s := range info.Streams {
		switch s.CodecType {
		case probe.TypeVideo:
			if media.IsCoverArt(s) {
				return false
			}
		case probe.TypeAudio:
		case probe.TypeSubtitle:
			if s.CodecName != "mov_text" && s.CodecName != "tx3g" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// streamCounts tallies the streams a gate expects to survive.
type streamCounts struct {
	video, audio, subtitle, attachment, data, other int
}

func countStreams(info *probe.Info) streamCounts {
	var c streamCounts
	for _, s := range info.Streams {
		switch s.CodecType {
		case probe.TypeVideo:
			c.video++
		case probe.TypeAudio:
			c.audio++
		case probe.TypeSubtitle:
			c.subtitle++
		case "attachment":
			c.attachment++
		case "data":
			c.data++
		default:
			c.other++
		}
	}
	return c
}

// gateOutput verifies the muxed file before the originals are touched:
// duration, exact stream inventory and the appended track's language.
func (t *tool) gateOutput(tmp string, in *inputs) error {
	outInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		return fmt.Errorf("cannot probe the aligned output: %v", err)
	}
	if d, ok := outInfo.Duration(); ok && !gates.DurationKept(d, in.refDur, 0.99) {
		return fmt.Errorf("aligned output (%.3fs) is much shorter than the reference (%.3fs) - keeping originals", d, in.refDur)
	}
	want := countStreams(in.refInfo)
	got := countStreams(outInfo)
	want.audio++
	if got != want {
		return fmt.Errorf("aligned output streams (video %d, audio %d, subs %d, attachments %d, data %d) do not match the plan (video %d, audio %d, subs %d, attachments %d, data %d) - keeping originals",
			got.video, got.audio, got.subtitle, got.attachment, got.data,
			want.video, want.audio, want.subtitle, want.attachment, want.data)
	}
	audios := outInfo.ByType()[probe.TypeAudio]
	if len(audios) != want.audio {
		return fmt.Errorf("aligned output has %d audio tracks, want %d - keeping originals", len(audios), want.audio)
	}
	tagged := false
	for _, a := range audios {
		if isDubbedTag(a.Stream.Tags) &&
			media.LangMatches(a.Stream.Tags.Language, media.ExpandWhitelist([]string{t.cfg.LangDub})) {
			tagged = true
		}
	}
	if !tagged {
		return fmt.Errorf("aligned output is missing the '%s' dubbed track - keeping originals", t.cfg.LangDub)
	}
	return nil
}

func hasDubSuffix(path, suffix string) bool {
	if suffix == "" {
		return false
	}
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return strings.HasSuffix(strings.ToLower(stem), strings.ToLower(suffix))
}

func withExt(path, ext string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

// pairingLabel describes the pairing rules used for this run.
func (t *tool) pairingLabel() string {
	base := "exact name, episode tag, title+year"
	if !t.cfg.NoContentPair {
		base += "; weak episode tags verified by content"
	}
	return base
}
