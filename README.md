# MediaTool 2.0

Single-binary media library toolkit for MKV/MP4 compaction and management on
Linux. One `media-tool` binary serves NVIDIA (NVENC), Intel (QSV/VAAPI) and
AMD (VAAPI via Mesa) machines alike — the encoder is probed at runtime, not
chosen at build time.

> Work in progress: 2.0 rework of the retired per-tool generation
> (hevcconverter/ac3converter/cleaner/dupfinder). Subcommands are being
> ported from the proven bash suite; behavior parity is the spec.

## Commands

| Command | Purpose |
|---|---|
| `convert` | re-encode H.264 video tracks to HEVC (nvenc → qsv → vaapi → x265 probe chain) |
| `clean` | remove audio/subtitle tracks of non-whitelisted languages (default: cs + en) |
| `ac3` | append AC3 640k compatibility tracks for DTS/TrueHD/FLAC audio |
| `dup` | report duplicate audio tracks (read-only) |
| `enhance` | AI-upscale via Real-ESRGAN and re-encode to HEVC 1080p |

All library-touching commands honor the dot ignore files in the target
directory, keep `.old` backups and support a dry run.

## Build

```shell
make build      # dist/media-tool
make test       # vet + staticcheck + race tests
```

## Requirements

* ffmpeg / ffprobe on PATH (or next to the binary / via `FFMPEG_BIN`)
* for hardware encoders: access to the GPU device (NVIDIA driver, or the
  `render` group for `/dev/dri/renderD128` on Intel/AMD)
* jq is not needed — 2.0 parses ffprobe JSON natively
