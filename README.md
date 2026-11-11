# MediaTool 2.0

Single-binary media library toolkit for MKV/MP4 compaction and management on
Linux. One `mediatool` binary serves NVIDIA (NVENC), Intel (QSV/VAAPI) and
AMD (VAAPI via Mesa) machines alike — the encoder is probed at runtime, not
chosen at build time. 2.0 replaces the retired per-tool generation
(hevcconverter/ac3converter/cleaner/dupfinder) and the interim bash suite
(hevc-convert.sh, hevc-tablet.sh and friends).

## Install

Grab a binary from [GitHub Releases](../../releases) (linux/amd64, static,
no runtime dependencies beyond ffmpeg/ffprobe), or build from source:

```shell
make build      # dist/mediatool
```

## Commands

| Command | Purpose |
|---|---|
| `convert` | re-encode video to HEVC/AV1 via presets (movie, tv, tablet, camera, lowq, chromecast) |
| `clean` | remove audio/subtitle tracks of non-whitelisted languages (default: cs + en) |
| `ac3` | append AC3 640k compatibility tracks for DTS/TrueHD/FLAC audio |
| `dup` | report duplicate audio tracks (read-only) |
| `enhance` | AI-upscale with Real-ESRGAN and re-encode to HEVC 1080p |
| `version` | print the version |

Run `mediatool <command> -h` for the full flag list of each command.

### convert

```
mediatool convert [-p movie|tv|tablet|camera|lowq|chromecast]
                  [-c hevc|av1] [-e auto|nvenc|qsv|vaapi|sw] [-q cq] [-r height]
                  [-a copy|aac[:bitrate[:channels]]|ac3[:bitrate]] [-tonemap]
                  [-vaapi_device dev] [-i list] [-f] [-n] <dir|file>
```

Presets bundle the common workflows; every preset value is overridable by
an explicitly passed flag (`-p tablet -r 1080p -q 28 -a copy`):

| preset | quality | scale | HDR | audio |
|---|---|---|---|---|
| `movie` (default) | CQ 26 quality target | – | kept | copy |
| `tv` | CQ 26 | 1080p | → SDR | AC3 compat appended |
| `tablet` | CQ 33 | 720p | → SDR | AAC 192k stereo |
| `camera` | CQ 28, capped at ~65 % of the source bitrate | – | – | AAC 128k |
| `lowq` | CQ 31, capped at ~50 % | – | – | copy |
| `chromecast` | CQ 22, 15M cap, level 4.1, written as a new `*_CC4_FIXED.mkv` (originals untouched) | – | – | copy |

- `-c av1` targets AV1 instead of HEVC (hardware `av1_nvenc`/`av1_qsv`/
  `av1_vaapi` where available, `libsvtav1` software fallback); HEVC stays
  the default output. The chromecast preset is HEVC-only (the target device
  decodes HEVC). GPUs without AV1 hardware encode (e.g. the Intel N100)
  fall back to slow software encoding.
- Sources: H.264 and VC-1 (`vc1`, including `wmv3`) convert without
  flags; anything else needs `-f`.
- **Tonemap** (`-tonemap`, on by default in tv/tablet): HDR10/HLG sources
  re-map to SDR bt709 (bt2390) — on NVENC via the GPU-resident CUDA chain
  (`tonemap_cuda`), otherwise in software through libplacebo when the
  ffmpeg build has it; with neither available, HDR files are skipped
  untouched. Without tonemapping, HDR10/HLG metadata is preserved (p010
  for 10-bit); Dolby Vision sources are skipped (`-f` forces, losing the
  DV layer); HDR10+ dynamic metadata is dropped (static HDR10 stays).
- **Audio target** (`-a`): `copy` keeps all audio as-is; `aac[:bitrate[:channels]]`
  transcodes non-AAC tracks (existing AAC tracks are copied; `-a aac:192k:2`
  = 192 kbps stereo); `ac3[:bitrate]` appends AC3 compatibility tracks by
  the `ac3` command's rules, in the same encode pass.
- **Track cleanup** (`-l cs,en`): audio and subtitle tracks outside the
  language whitelist are dropped during the encode, by the `clean`
  command's rules (whitelist expansion, untagged tracks always stay, sole
  tracks are never removed). The plan lines list every removal; a
  stream-count gate verifies the output. E.g. the classic one-shot library
  sweep: `mediatool convert -q 22 -l cs,en <dir>`.
- **Deinterlace is automatic**: sources flagged interlaced (field order
  tt/tb/bt/bb) are yadif-deinterlaced before scaling, on every backend.
- `-r 1080p` downscales only (never upscales), aspect ratio preserved.
- MKV sources stay MKV; every other container muxes to MP4 with
  `+faststart`. The walk picks up `.mkv/.mp4` plus the camera formats
  `.mts/.m2ts/.ts/.mpg/.mpeg/.avi/.mov/.wmv`; MPEG-TS/AVI inputs get
  `+genpts`. The converted file keeps the source's mtime.
- Files with several real video streams (3D MVC) are skipped; cover-art
  streams (mjpeg/png/attached_pic) are dropped from the encode.
- Retry ladder: hardware decode with subtitles → without → software decode
  (tonemapped HDR files use the CUDA chain first, then libplacebo).
- Outputs are adopted only when smaller and at least 99 % of the source
  duration (and, with appended AC3 tracks, only when every track survived);
  otherwise the original stays untouched.

### clean

```
mediatool clean [-l cs,en] [-e] [-i list] [-n] <dir|file>
```

Removes audio and subtitle tracks whose language tag is outside the
whitelist (`cs,en` expand to cs/ces/cze/cz and en/eng; missing/und/unknown
tags always count as kept). A stream is kept when it is the only track of
its type unless `-e`. Cover-art video streams are always removed. Pure
`-c copy` remux — never re-encodes. Verified by duration, size and
stream-count gates. Note: MP4 files usually carry no language tags, so
cleaning them is mostly a no-op except cover-art removal.

### ac3

```
mediatool ac3 [-b 448000] [-l lang] [-i list] [-n] <dir|file>
```

For audio in DTS/TrueHD/FLAC without an AC3 track of the same language,
appends an AC3 640k track (video and everything else copied). An existing
AC3 track counts as "already converted" for its language unless it is a
commentary track, below `-b` (bit/s, so the default 448000 = 448 kbps), or
has an unknown bitrate with fewer than 6 channels. E-AC3 is never converted
(both playback targets decode it).

### dup

```
mediatool dup [-i list] <dir>
```

Read-only report of audio tracks sharing codec, bitrate, channel count and
language tag within one file. Useful before `clean`.

### enhance

```
mediatool enhance [-q 22] [-engine path] [-vaapi_device dev] [-i list] [-n] <dir>
```

Extracts frames with hardware decoding, upscales them 2× with the
Real-ESRGAN anime model (ncnn Vulkan) and encodes to HEVC 1080p
(`scale=-2:1080`, aspect-preserving), written as MKV for both MKV and MP4
inputs. Output: `<stem>_enhanced_1080p_hevc.mkv` next to the source. The
engine binary is downloaded on first use to `~/.local/share/mediatool/`
(override with `-engine` or `REALESRGAN_BIN`). Frame directories are
cleaned on exit and on interrupt.

## Ignore files

A file is skipped when its path contains **any** entry from any of these
(union; one plain substring per line, matched literally against the full
path — directory and file names both count; `#` comments allowed):

| File | Read by |
|---|---|
| `.mediatool-ignore` in the target dir | all commands |
| `.mediatool-convert-ignore` / `.mediatool-clean-ignore` / `.mediatool-ac3-ignore` / `.mediatool-enhance-ignore` | their command — per-command ignore lists, e.g. "not to clean" vs "not to re-encode" |
| `.hevcconverter-ignore` / `.cleaner-ignore` / `.ac3converter-ignore` / `.enhance-ignore` | deprecated pre-2.0 per-command names, still honored with a rename warning |
| `-i "a,b"` CLI list | all commands |

Files are looked up in the target directory **and every ancestor up to the
filesystem root** (like a flat gitignore hierarchy; for a single-file target
the search starts at the file's directory), so an ignore file in `/foo` or
`/foo/bar` also governs `mediatool clean /foo/bar/baz`.

## Conventions

- **`.old` backups**: in-place replacements never clobber an existing
  `<file>.old` — convert skips such files up front; clean and ac3 refuse
  the swap and keep the original when one exists.
- **Safety gates**: outputs are adopted only after duration, size and
  stream-count checks.
- **Dry run**: every modifying command accepts `-n` and prints its plan.

## Output & colors

When stdout and stderr are both terminals, the output renders for humans:
a settings header, a line per processed file with a status symbol (green ✓
processed, red ✗ failed, yellow ⚠ notices), `[i/n]` position markers, a
per-file progress bar driven by ffmpeg's own `time=`/`speed=` stats
(convert, clean, ac3, enhance), failure lines with the captured ffmpeg
error tail, and a closing summary with size aggregates, the failure list
and elapsed time. The enhance engine download shows a byte progress bar.

By default only files that are (or would be) processed get a permanent
line; skipped/untouched files collapse into a single dim "walking" line
that rewrites in place (`– [i/n] path — reason`) so you can see the run
moving through the library without a scroll of gray skips. `-v` shows
every skip/OK line permanently and swaps the progress bar for ffmpeg's
raw stats passthrough.

Piped or redirected output stays plain — line-for-line identical to the
earlier releases — so scripts and greps keep working. Color control
follows the usual conventions: `NO_COLOR` disables colors, `CLICOLOR_FORCE=1`
forces them, `TERM=dumb` disables them.

## Requirements

- ffmpeg / ffprobe (`FFMPEG_BIN`/`FFPROBE_BIN` env overrides, else
  Jellyfin's build, common locations, PATH)
- hardware encoders: NVIDIA driver for nvenc; the `render` group for
  `/dev/dri/renderD128` on Intel/AMD (VAAPI); Intel mfx runtime for QSV.
  A missing backend is probed away — everything falls back to libx265.
- jq is not needed — 2.0 parses ffprobe JSON natively

## Development

```shell
make install   # staticcheck
make test      # vet + staticcheck + race tests
make lint      # gofmt check
make build     # dist/mediatool
```

CI runs the same checks on every push/PR; tagged releases (`v*`) build the
linux/amd64 binaries via GoReleaser and publish them to GitHub Releases.
