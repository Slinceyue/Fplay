package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LRC:同名 .lrc,时间标签 [mm:ss.xx](允许一行多个标签)。
type lrcLine struct {
	T    float64 // 秒
	Text string
}

var timeTagRe = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

func parseLRCFile(songPath string) []lrcLine {
	lrc := songPath[:len(songPath)-len(filepath.Ext(songPath))] + ".lrc"
	b, err := os.ReadFile(lrc)
	if err != nil {
		return nil
	}
	return parseLRC(string(b))
}

func parseLRC(text string) []lrcLine {
	var out []lrcLine
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		tags := timeTagRe.FindAllStringSubmatchIndex(line, -1)
		if len(tags) == 0 {
			continue
		}
		// 文字 = 最后一个标签结束之后的部分
		lastEnd := tags[len(tags)-1][1]
		content := strings.TrimSpace(line[lastEnd:])
		for _, m := range tags {
			mm := line[m[0]:m[1]]
			sub := timeTagRe.FindStringSubmatch(mm)
			t := parseTag(sub)
			if t >= 0 {
				out = append(out, lrcLine{T: t, Text: content})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

func parseTag(s []string) float64 {
	if len(s) < 3 {
		return -1
	}
	mi, err1 := strconv.Atoi(s[1])
	se, err2 := strconv.Atoi(s[2])
	if err1 != nil || err2 != nil {
		return -1
	}
	frac := 0.0
	if len(s) >= 4 && s[3] != "" {
		if len(s[3]) == 1 {
			frac = float64(s[3][0]-'0') / 10
		} else if len(s[3]) == 2 {
			frac = float64(s[3][0]-'0')/10 + float64(s[3][1]-'0')/100
		} else {
			frac = float64(s[3][0]-'0') / 10 // 三位的按前两位近似
		}
	}
	return float64(mi*60+se) + frac
}

// curLRC 返回当前秒对应的行号(落在某行时间段内的最大行)。
func curLRC(ls []lrcLine, t float64) int {
	idx := -1
	for i, l := range ls {
		if t+0.3 >= l.T {
			idx = i
		} else {
			break
		}
	}
	return idx
}
