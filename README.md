# MediaTool

## ac3converter

Application searches for MKV (Matroska) files which contain audio streams
in DTS, TrueHD or E-AC3 codecs. If such a file does not contain AC3 stream
of the same language (except commentary tracks), file is being converted
and AC3 track is appended in the file.

# hevcconverter

Application searches for MKV (Matroska) files which contain video streams
in H.264 codec. If such a file does not contain HEVC stream, file is being
converted and HEVC track replaces the original one.

## dupfinder

Application searches for duplicated audio tracks in MKV (Matroska) files.

# cleaner

Application searches for MKV (Matroska) files which contain streams
without whitelisted language. Such streams are being removed from the file.

### Requirements

* ffmpeg
* ffprobe (installed along with ffmpeg)
* user is in the group `render` (to access `/dev/dri/renderD128` for hardware acceleration)

```shell
sudo apt-get update
sudo apt-get install ffmpeg
```

### Usage

```
make
./dist/ac3converter -help
./dist/hevcconverter -help
./dist/dupfinder -help
```
