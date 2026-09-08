package main

import (
	"strings"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// readTags 读 FLAC 的 Vorbis 注释(TITLE/ARTIST),没有则退回文件名。
func readTags(path string) (title, artist string) {
	st, err := flac.ParseFile(path)
	if err != nil {
		return "", ""
	}
	for _, b := range st.Blocks {
		vc, ok := b.Body.(*meta.VorbisComment)
		if !ok {
			continue
		}
		for _, tag := range vc.Tags {
			k := strings.ToUpper(strings.TrimSpace(tag[0]))
			v := strings.TrimSpace(tag[1])
			switch k {
			case "TITLE":
				if title == "" {
					title = v
				}
			case "ARTIST":
				if artist == "" {
					artist = v
				}
			}
		}
	}
	return title, artist
}
