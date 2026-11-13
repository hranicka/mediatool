# Changelog

## 2.2.0

- **`align` timing reworked: one robust shared-speed map instead of a
  piecewise guess** — dubs and originals are the same edit up to one
  global speed difference (PAL speedup, NTSC transfers, either
  direction), one time shift (a shorter or longer intro or credits), and
  a few mid-video cuts (TV broadcasts trim scenes for their timeslot).
  The old walk + piecewise machinery tried to cope with all of that one
  heuristic at a time and got the lipsync wrong; the new fit estimates
  the speed once (Theil–Sen over short-baseline pairs, immune to the
  cut bias that plagued long baselines), locates the offset jumps,
  refines every segment with a constrained walk at the full sampling
  grid, and keeps only the matches that agree with their segment. Cut
  boundaries land on the exact match where the offset flips, removed
  scenes stay silent on the dub track, and the report prints the fitted
  speed with its known-conversion label (PAL speedup etc.), every
  segment with its offset and residual, and each silent gap. The run
  validates the map (inlier count and span support, median and p95
  residual, per-segment residuals and tilt, plausible speed,
  fragmentation) and refuses loudly instead of producing wrong lipsync;
  the duration ratio is cross-checked against the detected cuts but
  never refuses.
- **`align` audio calibration** — frame matching aligns the dub's video,
  but a capture's audio stream can sit at its own offset against its own
  video (measured 0.12–0.42 s across a real Czech TV capture library,
  different per episode), leaving the whole dub track out of lipsync no
  matter how good the frame fit is. After the first resample, the shared
  music/effects bed between the language versions is cross-correlated
  against the reference audio in 30 s windows (raw waveform FFT,
  parabolic peak refinement), each segment's median lag becomes its
  calibration constant, and a second resample verifies it settled.
  Segments without stable audio evidence inherit their neighbour's
  correction; the report prints the measured per-segment calibration.
  Post-calibration sync on the real library: median residual ~1 ms, max
  19 ms on two episodes; the third carries a ~54 ms sub-frame audio edit
  inside one minute (reported as measured). Verified on a real EN WEB-DL vs Czech TV capture
  library: every episode fits at PAL speedup 23.976→25 with 20 ms
  residual, and each broadcast's 2–3 scene trims land as digital silence
  at the exact mapped positions.
- **`align` lipsync precision** — the default frame sampling interval drops
  from 0.2 s to 0.05 s (20 fps), which limits the timing map to about
  ±25 ms instead of ±100 ms. Each segment line now reports its fitted
  residual, and the map prints the sampling grid and worst residual so a
  weak map is visible before muxing. A lost scene reattaches at the first
  matching frame (no stride grid), segment joins are forced monotonic and
  small match losses with a continuous offset are bridged, so only real
  missing scenes stay silent. The report lists every gap above 20 ms with
  the total and longest silent span. A dubbed audio stream that starts
  after its first video frame (common in broadcast TS) keeps that lead as
  silence instead of shifting the whole track. Frame hashing now streams
  ffmpeg output instead of buffering every sampled frame, Hamming distance
  uses 64-bit popcounts, and run fitting is incremental, keeping the finer
  sampling cheap in memory and CPU.
- **`align` content-aware pairing** — a pair that rests only on an episode
  tag with a different title (or a fuzzy title) is now verified against
  the video before the expensive timing pass. Short frame samples spread
  over both files are fingerprinted and cached; when another episode
  matches clearly better, the plan is repaired and the report says
  `re-paired by content`. This handles releases whose dub numbering skips
  episodes (on The Simpsons S36, Czech `S36E12` is English `S36E11`),
  which previously produced a zero-match timing map. Strong name matches
  and `-pair` entries are never changed; `-no-content-pair` disables the
  check.
- **`align` late duplicate shots** — a resync candidate may no longer
  leave far less reference than the dub still to play, and resync
  confirmation now really waits for the requested consecutive frames.
  A repeated shot near the end of the reference (for example the closing
  credits) could previously hijack the walk and make the remaining
  minutes silently unmapped; the same Simpsons couple now covers about
  90 % instead of 70 %.
- **`align` frame cache** — the sampling interval is now part of the cache
  key, so a dry run at one resolution no longer evicts the entry used by
  another.
- **`align` path handling** — a leading `~` in a positional path is
  expanded even when the shell did not do it (quoted paths), and a
  reference or dub that cannot be read is reported by name with the OS
  reason instead of a generic "both arguments must exist".
- **`align` batch mode** — pairs a whole directory of references with
  dubbed versions and aligns each couple in place: the reference is
  replaced by the aligned file, the original reference and the dub are
  kept as `.old` (`-no-backup` replaces the reference and deletes the
  dub). Pairing uses exact normalized names, episode tags (`S06E02`,
  `6x02`, `S2E5`, `Season 6 Episode 2`) or movie title+year, ignoring
  release and language markers. A matching episode tag alone is enough,
  even when the show names differ (marked `title differs` with a verify
  notice); ambiguous names are never guessed, missing counterparts are
  reported and skipped, and `-pair ref=dub` resolves the rest.
  `-pair-only` prints the plan before decoding; `-n` is a full dry run. A
  single directory works with `-dub-suffix .cz`. The new
  `.mediatool-align-ignore` file is honored in both directories.
- **`align` mixed file + directory input** — `align <reference>
  <dubdir>` (or the reverse) searches the directory with the normal
  episode/title pairing and processes the single match as a one-pair
  batch run: the matched reference is replaced in place with `.old`
  backups (`-no-backup` removes both originals). This is the "one
  episode, a folder of dubs" workflow; `-pair-only`, `-n`, `-pair` and
  `-i` apply, and a missing counterpart skips with the closest
  suggestion.
- **`align` piecewise timing map** — the global quadratic fit is replaced
  by affine segments with outlier rejection. The walk now resynchronizes
  across long intros, skipped scenes and dub-only inserts instead of
  staying inside the old ±10 s window; reference regions with no dub
  counterpart stay silent on the dubbed track and dub-only regions are
  dropped. The run reports coverage, segments and gaps and refuses maps
  covering less than half of either file, replacing the old 80–125 %
  span check.
- **`align` keeps every reference stream** — the mux now copies all
  reference audio tracks, subtitles, attachments/fonts, chapters and
  metadata (previously only `a:0` and subtitles), selects the dub track
  by `-lang-dub`, and tags only the appended track. `-lang-ref` is used
  as a sanity check for multi-audio references.
- **`align` frame-hash cache** — the expensive perceptual hashing is
  cached under `$XDG_CACHE_HOME/mediatool/align` (keyed by path, size,
  mtime and interval), so a `-n` run populates it and the following real
  run skips both full decodes. Only the 1024-bit hashes are stored, never
  video frames. `-no-cache` disables the cache, `-no-backup` removes the
  entries of the swapped files, and `align -clean-cache [-n]` clears the
  cache (including temp files left by an interrupted write).

- **clean no longer gates on size** — a removal-only `-c copy` remux copies
  the payload verbatim, so the output size only reflects muxer layout
  economics (audio lacing, cluster and index granularity): remuxing a
  mkvmerge-muxed file with ffmpeg can legitimately grow by a few MB and
  still be a correct clean, yet the size gate rejected it
  (`output is not smaller (15.9G -> 16G)`). clean's output is now verified
  by the duration and exact stream-count gates alone; any growth shows in
  the Cleaned size line. Re-encode outputs (`convert`) still must shrink.

## 2.1.1

- **clean tolerates the remux index overhead for tiny removals** — removing
  a small stream (cover art, a lone subtitle track) from a cue-less source
  loses to the seek index the remux rewrites (measured ~14 KB for 750
  keyframes), so the strict smaller-size gate rejected the clean on every
  run and the file was never touched. The size gate now allows the source
  size plus a modeled index budget: one index entry per frame in the worst
  case (duration × fps of the first real video stream) plus a 64 KB
  allowance for tags and headers. Re-encode outputs still must shrink.

## 2.1.0

- **`-no-backup` for convert/clean/ac3** — replaces files in place without
  creating a `.old` backup; the original is removed only after the new
  file is safely in place (the source's mtime still carries over). The
  run header shows the effective `Backups` setting.
- **`clean -old`** — a deletion mode that removes the `.old` backup files
  of previous runs instead of cleaning tracks: a recursive scan for
  `*.old`, honoring the ignore rules and `-n`, single-file targets
  allowed (non-backup files are refused). The summary reports the freed
  space; removals never collapse into the quiet walking line.
- **`dedup`** — a new read-only content-duplicate report: finds files that
  carry the same movie/episode across encodes, sizes, extensions and
  names (`.old` backups excluded). Samples a frame every `-interval`
  seconds, perceptually hashes each one (median-threshold grayscale,
  robust against encoder noise and resolution changes) and matches the
  sequences with a windowed walk tolerant to shifted starts, cut scenes
  and framerate variants. A duration prefilter keeps frame extraction to
  files with at least one duration-compatible peer. Groups print with a
  suggested keeper (highest resolution, then the larger file) and the
  reclaimable space; deletion stays manual.
- **`align`** — a new command appending a dubbed audio track to a
  reference video by scene matching (port of align_audio.py): perceptual
  frame hashes from both files, windowed matching, a quadratic-fit
  monotonic timing map with outlier rejection, and a streaming resampler
  that maps the dub onto the reference timeline before muxing it as a
  tagged AAC track next to the untouched reference video, audio and
  subtitles. `-n` prints the timing map (endpoints and 10 sample offsets)
  without muxing. The frame hashing is shared with dedup via the new
  `internal/vhash` package.
- **Robustness hardening for align/dedup** (found by a code review plus
  measured hash-distance distributions): the align resampler no longer
  drops the `s0` interpolation term on stretched maps — a PAL-speedup dub
  buzzed at ~4% of all samples before; align refuses implausible timing
  maps (reference span outside 80–125% of the dub duration) and warns
  when frames are highly self-similar (synthetic gradients measure ~68
  background bits vs ~470–520 for real content, so matches there map
  noise however small their distance); dedup annotates such groups,
  notes files without a container duration (they pair with everything),
  re-checks duration compatibility during pairing, orders groups
  deterministically and validates its numeric flags; align validates its
  flags, requires usable durations and reference audio up front (both
  before any decoding), and notes attachment streams the mux does not
  copy. READMEs state the ±window bound on shifted starts.
- **Clearer convert settings header** — the run header now spells out the
  full effective plan, defaults included, so a run is self-describing: a
  dedicated `Quality` line names the strategy (quality target, the
  camera/lowq source-bitrate cap, or the Chromecast 15M/level-4.1 cap),
  and `Scale`, `Tonemap` and `Languages` always show their effective
  value (`keep source resolution`, `off (keep HDR)`, `all`) instead of
  appearing only when non-default. `VAAPI device` and `Force` appear when
  relevant. The CQ value moved out of the encoder line into `Quality`.
- **Aligned `-p`/`-q` help** — both flags now list the presets in the same
  order (movie, tv, tablet, camera, lowq, chromecast), and the `-q`
  defaults are generated from the presets table so the help can never
  drift from the actual defaults again. The redundant inline `(default)`
  marker on `-p` is gone (the flag's own `(default "movie")` remains).
- **Interactive terminal output** — when stdout and stderr are terminals,
  every command renders for humans: colored status symbols per file
  (✓ processed, – skipped, ✗ failed, ⚠ notices), `[i/n]` position
  markers, a per-file progress bar driven by ffmpeg `time=`/`speed=`
  stats (convert/clean/ac3/enhance), failure lines with the captured
  ffmpeg error tail, a first-run engine-download progress bar (enhance)
  and an end-of-run summary with size aggregates, the failure list and
  elapsed time. Piped or redirected output is byte-identical to the
  previous plain rendering (pinned by a golden test), so automation is
  unaffected. `NO_COLOR`, `CLICOLOR_FORCE` and `TERM=dumb` are honored;
  `-v` keeps the raw ffmpeg stats passthrough instead of the bar.
- Interrupting a run (Ctrl-C) prints `interrupted; cleaning up temp
  files` before exiting 130 instead of dying silently.
- **Quieter interactive output** — skipped/untouched files no longer
  scroll a gray line each; they collapse into one dim "walking" line
  rewritten in place (`– [i/n] path — reason`) so progress stays visible
  without the noise. Only processed (or would-be-processed) files get a
  permanent line. `-v` restores every skip/OK line. Plain output is
  unchanged.
- **Fixed: language whitelist dropped full-word tags** — files tagged
  with the language name (`English`, `Czech`) instead of the ISO code
  (`eng`, `cze`) failed the `cs,en` whitelist and had those tracks
  removed by `clean` (and `convert -l`). Language names now normalize to
  their codes on both the tag and whitelist sides, so both spellings
  match.
- **Sole-track keeps are now flagged** — when `clean` (or `convert -l`)
  keeps a foreign audio/subtitle track because it is the only one of its
  type, it prints a yellow ⚠ notice instead of hiding the decision. This
  also surfaces the case where the file is otherwise untouched ("nothing
  to remove"), which previously gave no hint that a non-whitelisted track
  survived.
- **Fixed: broadcast "original audio" dropped** — the reserved ISO 639-2
  local-use codes `qaa`–`qtz` (used by DVB broadcasts for the original /
  undefined soundtrack, commonly `qaa`) are now always kept by the
  language whitelist, like `und`. Previously `clean` and `convert -l`
  removed them when not cs/en, dropping the original audio of TV
  recordings.
- **clean skips broadcast dump containers** — `.ts/.mts/.m2ts/.mpg/.mpeg`
  are skipped (with a reason) instead of remuxed: clean only maps
  video/audio/subtitles, so it silently dropped teletext/data streams and
  rewrote the container under the original name. Use `convert` for these
  formats; clean handles `.mkv`/`.mp4`.
- **convert: lossless container selection + matching filename** — non-MKV
  sources now mux to MP4 only when they carry video+audio alone; anything
  with subtitles, teletext/data or attachments goes to MKV so nothing is
  lost (previously everything became MP4, dropping DVB subtitles/teletext).
  The output extension follows the container (`part1.ts` → `part1.mkv`,
  `cam.ts` → `cam.mp4`), the original is kept as `<source>.old`, and the
  rename is reported as an `output:` line. MKV sources and Chromecast mode
  are unchanged.
- **Higher-quality NVENC encoding** — the NVENC quality block now adds
  temporal AQ, 32-frame lookahead and fullres multipass, spending bits
  better on motion and grain at the same CQ (HEVC and AV1).
- **movie preset default quality raised to CQ 22** (from 26) — a
  quality-leaning default that still compacts well, keeping the source
  resolution and HDR (audio copy). Override with `-q` (`-q 18` for
  action/grainy film or near-transparent 1080p/4K HDR, `-q 26` for smaller
  files).

## 2.0.1

- **Fixed: HDR detection never fired on real files** — the ffprobe parser
  read the `color_trc` JSON key, but ffprobe (any version) emits
  `color_transfer`. HDR10/HLG sources were re-encoded without color
  signaling and the HDR notes never printed; the tests passed only
  because their fixtures hand-wrote the wrong key. With the fix, HDR
  metadata is preserved (or tonemapped) as documented.
- `convert`: **presets replace the quality modes** — `-p` now selects a
  whole workflow: `movie` (default, HDR kept, audio copied), `tv` (1080p
  downscale, HDR→SDR, AC3 compat appended), `tablet` (720p, CQ 33,
  HDR→SDR, AAC 192k stereo), `camera` (bitrate-capped, AAC 128k), plus
  `lowq` and `chromecast`. Every preset value is overridable by an
  explicitly passed flag (`-p tablet -r 1080p -q 28 -a copy`).
- `convert`: optional **HDR→SDR tonemapping** (`-tonemap`, enabled by the
  tv/tablet presets) — HDR10/HLG sources re-map to SDR bt709 with bt2390:
  GPU-resident CUDA chain (`tonemap_cuda`) on NVENC, libplacebo in
  software on every other backend (probed; missing → the file is skipped
  untouched instead of silently keeping HDR).
- `convert`: **audio targets** (`-a copy|aac[:bitrate[:channels]]|ac3[:bitrate]`)
  — AAC transcodes non-AAC tracks (existing AAC tracks are copied) with an
  optional stereo downmix; `ac3` appends AC3 compatibility tracks by the
  `ac3` command's rules in the same encode pass, verified by an
  audio-count gate.
- `convert`: **track cleanup** (`-l cs,en`) — audio and subtitle tracks
  outside the language whitelist are dropped during the encode by the
  `clean` command's rules (extracted into the shared `clean.PlanTracks`),
  with plan lines and a stream-count gate. Combined with `-q`, this
  restores the flexible one-shot workflow (`mediatool convert -q 22
  -l cs,en <dir>`).
- `convert`: **auto-deinterlace** — sources flagged interlaced (field
  order tt/tb/bt/bb) are yadif-deinterlaced before scaling on every
  backend (`yadif_cuda` in the CUDA chain).
- The media walk picks up the camera formats `.mts/.m2ts/.ts/.mpg/.mpeg/
  .avi/.mov/.wmv` in all commands (single-file targets included).
  `convert` muxes non-MKV sources to MP4 (`+faststart`), applies
  `+genpts` to MPEG-TS/AVI inputs, accepts a single file as its target,
  and the converted file keeps the source's mtime.
- `convert`: simpler bitrate caps — sources whose cap would fall below a
  sane minimum (~800k camera / ~600k lowq) get no cap at all (pure
  quality targeting, size gate as backstop) instead of floor-clamped caps
  that could sit above the source bitrate, where they constrained
  nothing. The container-bitrate probe tier is gone (it measured audio
  and subtitles too; the size/duration estimate covers it).
- `convert`: new `-c` output codec flag — `av1` targets AV1
  (av1_nvenc/av1_qsv/av1_vaapi, libsvtav1 software fallback) while HEVC
  stays the default; the chromecast preset rejects AV1 (Chromecast 4
  devices decode HEVC only). The `-e` software encoder value is now `sw`
  (`x265` remains as a deprecated alias).
- `convert`: VC-1 sources (`vc1`, including `wmv3`) now convert without
  `-f`, like H.264 sources.
- Dot ignore files are honored along the whole target path: the search
  walks from the target directory up to the filesystem root (like a flat
  gitignore hierarchy), so an ignore file in `/foo` or `/foo/bar` also
  governs `mediatool clean /foo/bar/baz`.
- Per-command ignore lists next to the shared `.mediatool-ignore`:
  `.mediatool-convert-ignore`, `.mediatool-clean-ignore`,
  `.mediatool-ac3-ignore`, `.mediatool-enhance-ignore` — a path can be
  excluded from one command ("not to clean") while others still process it
  ("re-encode").
- The pre-2.0 per-command names (`.hevcconverter-ignore`,
  `.cleaner-ignore`, `.ac3converter-ignore`, `.enhance-ignore`) are
  deprecated but still honored; every hit prints a `warning:` line
  suggesting the rename. `.video-scripts-ignore` (bash-suite era) and
  dup per-command files are gone — dup is a read-only report, so only the
  shared file and `-i` apply to it.

## 2.0.0

Complete rework: the four per-tool binaries (hevcconverter, ac3converter,
cleaner, dupfinder) and the interim bash suite collapse into a single
`mediatool` binary with subcommands.

- `convert` — HEVC conversion with the runtime encoder probe
  (nvenc → qsv → vaapi → x265), movie/camera/lowq/chromecast modes, HDR
  preservation, Dolby Vision skip, safety gates and retry ladder
  (supersedes hevcconverter; adds VAAPI for Intel and AMD GPUs).
- `clean` — non-whitelisted-language track removal (cs/en default) as a
  pure remux with stream-count gates.
- `ac3` — AC3 640k compatibility-track appending; E-AC3 is deliberately
  never converted.
- `dup` — duplicate audio track report.
- `enhance` — Real-ESRGAN pipeline with aspect-preserving 1080p output and
  a natively downloaded engine.
- Shared: dot ignore files, `.old` backups with clobber protection, dry
  runs, interrupt-safe temp cleanup, runtime encoder probing, Go tests
  with fake ffmpeg/ffprobe, GitHub Actions CI and GoReleaser-based
  releases (linux/amd64).
