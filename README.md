# MediaTool 2.0

Single-binary media library toolkit for MKV/MP4 compaction and management on
Linux. One `media-tool` binary serves NVIDIA (NVENC), Intel (QSV/VAAPI) and
AMD (VAAPI via Mesa) machines alike — the encoder is probed at runtime, not
chosen at build time. 2.0 replaces the retired per-tool generation
(hevcconverter/ac3converter/cleaner/dupfinder) and the interim bash suite.

## Install

Grab a binary from [GitHub Releases](../../releases) (linux/amd64, static,
no runtime dependencies beyond ffmpeg/ffprobe), or build from source:

```shell
make build      # dist/media-tool
```

## Commands

| Command | Purpose |
|---|---|
| `convert` | re-encode H.264 video tracks to HEVC (modes: movie, camera, lowq, chromecast) |
| `clean` | remove audio/subtitle tracks of non-whitelisted languages (default: cs + en) |
| `ac3` | append AC3 640k compatibility tracks for DTS/TrueHD/FLAC audio |
| `dup` | report duplicate audio tracks (read-only) |
| `enhance` | AI-upscale with Real-ESRGAN and re-encode to HEVC 1080p |
| `version` | print the version |

Run `media-tool <command> -h` for the full flag list of each command.

### convert

```
media-tool convert [-p movie|camera|lowq|chromecast] [-e auto|nvenc|qsv|vaapi|x265]
                   [-q cq] [-r height] [-vaapi_device dev] [-i list] [-f] [-n] <dir>
```

- `movie` (default): CQ 26 quality target. `camera`: CQ 28, capped at ~65 %
  of the source video bitrate. `lowq`: CQ 31, capped at ~50 %.
  `chromecast`: HEVC main@4.1 with a 15M ceiling, written as a new
  `*_CC4_FIXED.mkv` (originals untouched).
- HDR10/HLG color metadata is preserved (p010 for 10-bit); Dolby Vision
  sources are skipped (`-f` forces, losing the DV layer); HDR10+ dynamic
  metadata is dropped (static HDR10 stays).
- `-r 1080p` downscales only (never upscales), aspect ratio preserved.
- Files with several real video streams (3D MVC) are skipped; cover-art
  streams (mjpeg/png/attached_pic) are dropped from the encode.
- Retry ladder: hardware decode with subtitles → without → software decode.
- Outputs are adopted only when smaller and at least 99 % of the source
  duration; otherwise the original stays untouched.

### clean

```
media-tool clean [-l cs,en] [-e] [-i list] [-n] <dir|file>
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
media-tool ac3 [-b 448000] [-l lang] [-i list] [-n] <dir|file>
```

For audio in DTS/TrueHD/FLAC without an AC3 track of the same language,
appends an AC3 640k track (video and everything else copied). An existing
AC3 track counts as "already converted" for its language unless it is a
commentary track, below `-b` (bit/s, so the default 448000 = 448 kbps), or
has an unknown bitrate with fewer than 6 channels. E-AC3 is never converted
(both playback targets decode it).

### dup

```
media-tool dup [-i list] <dir>
```

Read-only report of audio tracks sharing codec, bitrate, channel count and
language tag within one file. Useful before `clean`.

### enhance

```
media-tool enhance [-q 22] [-engine path] [-vaapi_device dev] [-i list] [-n] <dir>
```

Extracts frames with hardware decoding, upscales them 2× with the
Real-ESRGAN anime model (ncnn Vulkan) and encodes to HEVC 1080p
(`scale=-2:1080`, aspect-preserving), written as MKV for both MKV and MP4
inputs. Output: `<stem>_enhanced_1080p_hevc.mkv` next to the source. The
engine binary is downloaded on first use to `~/.local/share/media-tool/`
(override with `-engine` or `REALESRGAN_BIN`). Frame directories are
cleaned on exit and on interrupt.

## Ignore files

A file is skipped when its path contains **any** entry from any of these
(union; one plain substring per line, matched literally against the full
path — directory and file names both count; `#` comments allowed):

| File | Read by |
|---|---|
| `.media-tool-ignore` in the target dir | all commands |
| `.video-scripts-ignore` in the target dir | all commands (bash-suite era compat) |
| `.hevcconverter-ignore` / `.cleaner-ignore` / `.ac3converter-ignore` / `.enhance-ignore` / `.dupfinder-ignore` | their command |
| `-i "a,b"` CLI list | all commands |

Files are read from the top of the passed directory only (for a single-file
target: the file's directory).

## Conventions

- **`.old` backups**: in-place replacements never clobber an existing
  `<file>.old` — convert skips such files up front; clean and ac3 refuse
  the swap and keep the original when one exists.
- **Safety gates**: outputs are adopted only after duration, size and
  stream-count checks.
- **Dry run**: every modifying command accepts `-n` and prints its plan.

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
make build     # dist/media-tool
```

CI runs the same checks on every push/PR; tagged releases (`v*`) build the
linux/amd64 binaries via GoReleaser and publish them to GitHub Releases.
