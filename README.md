# MediaTool

## dtsconverter

Application searches for MKV (Matroska) files which contain audio streams
in DTS, TrueHD or E-AC3 codecs. If such a file does not contain AC3 stream
of the same language (except commentary tracks), file is being converted
and AC3 track is appended in the file.

## dupfinder

Application searches for duplicated audio tracks in MKV (Matroska) files.

### Requirements

* ffmpeg
* ffprobe (installed along with ffmpeg)

### Usage

```
make
./dist/dtsconverter -help
./dist/dupfinder -help
```
