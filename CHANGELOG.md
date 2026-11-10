# Changelog

## Unreleased

- `convert`: new `-c` output codec flag — `av1` targets AV1
  (av1_nvenc/av1_qsv/av1_vaapi, libsvtav1 software fallback) while HEVC
  stays the default; chromecast mode rejects AV1 (Chromecast 4 devices
  decode HEVC only). The `-e` software encoder value is now `sw` (`x265`
  remains as a deprecated alias).
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
