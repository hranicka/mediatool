# Changelog

## 2.0.4

- dup no longer reads per-command ignore files: it is a read-only report,
  so only the shared `.mediatool-ignore` and the `-i` list apply. The
  `.mediatool-dup-ignore` / `.dupfinder-ignore` names introduced or
  inherited earlier are gone; they never existed in the pre-2.0 tooling
  (the old dupfinder had no ignore support at all).

## 2.0.3

- New per-command ignore file names following the subcommands:
  `.mediatool-convert-ignore` (convert), `.mediatool-clean-ignore` (clean),
  `.mediatool-ac3-ignore` (ac3), `.mediatool-enhance-ignore` (enhance),
  `.mediatool-dup-ignore` (dup). The shared file stays
  `.mediatool-ignore`; per-command lists let a path be excluded from one
  command ("not to clean") while others still process it ("re-encode").
- The pre-2.0 per-command names (`.hevcconverter-ignore`,
  `.cleaner-ignore`, `.ac3converter-ignore`, `.enhance-ignore`,
  `.dupfinder-ignore`) are deprecated but still honored; every hit prints
  a `warning:` line suggesting the rename.

## 2.0.2

- Dropped `.video-scripts-ignore` support (bash-suite era). The shared
  ignore file is `.mediatool-ignore`.
- The per-command ignore files (`.hevcconverter-ignore` for convert,
  `.cleaner-ignore` for clean, `.ac3converter-ignore` for ac3,
  `.enhance-ignore` for enhance, `.dupfinder-ignore` for dup) are a normal
  feature, not legacy: they give each command its own ignore list (e.g.
  "not to clean" vs "not to re-encode") at every path level.

## 2.0.1

- Dot ignore files are now honored along the whole target path: the search
  walks from the target directory up to the filesystem root (like a flat
  gitignore hierarchy), so an ignore file in `/foo` or `/foo/bar` also
  governs `mediatool clean /foo/bar/baz`. Previously only the top of the
  passed directory was read.

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
- Shared: dot ignore files (union of shared + legacy per-tool names),
  `.old` backups with clobber protection, dry runs, interrupt-safe temp
  cleanup, runtime encoder probing, Go tests with fake ffmpeg/ffprobe,
  GitHub Actions CI and GoReleaser-based releases (linux/amd64).
