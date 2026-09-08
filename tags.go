package main

import (
	"os"
	"strings"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// readCover 抽出 FLAC 内嵌封面写到临时文件,返回文件路径;没有封面返回 ""。
func readCover(path string) string {
	st, err := flac.ParseFile(path)
	if err != nil {
		return ""
	}
	for _, b := range st.Blocks {
		pic, ok := b.Body.(*meta.Picture)
		if !ok || len(pic.Data) == 0 {
			continue
		}
		ext := ".jpg"
		mime := strings.ToLower(pic.MIME)
		if strings.Contains(mime, "png") {
			ext = ".png"
		}
		f, err := os.CreateTemp("", "fplay-cover-*"+ext)
		if err != nil {
			return ""
		}
		if _, err := f.Write(pic.Data); err != nil {
			_ = f.Close()
			return ""
		}
		_ = f.Close()
		return f.Name()
	}
	return ""
}

// readTags 读 FLAC 的 Vorbis 注释(TITLE/ARTIST)。
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
