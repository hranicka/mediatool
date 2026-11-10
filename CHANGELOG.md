# Changelog

## Unreleased

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
