package main

import (
	"fmt"

	"github.com/pixelbender/go-matroska/matroska"
)

type Track struct {
	Type  uint8
	Lang  string
	Codec string
	Name  string
}

func main() {
	path := "/media/hrani/apu/download/avatar/Avatar.2009.Extended.CZ.EN.UHD.VISIONPLUSHDR-X.mkv"

	doc, err := matroska.Decode(path)
	if err != nil {
		fmt.Println(err)
		return
	}

	for _, t := range doc.Segment.Tracks {
		for _, te := range t.Entries {
			track := &Track{
				Type:  uint8(te.Type),
				Lang:  te.Language,
				Codec: te.CodecID,
				Name:  te.Name,
			}
			fmt.Printf("%+v\n", track)
		}
	}
}
