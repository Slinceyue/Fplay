//go:build linux

package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// deviceFormat 从 /proc/asound 读当前正在播的 ALSA 设备格式,返回采样率/位深/声道。
// dev 形如 hw:1,0 或 hw:CARD=ECHOA,DEV=0;没在播返回 ok=false。
func deviceFormat(dev string) (rate, bits, ch int, ok bool) {
	dev = strings.TrimPrefix(dev, "hw:")
	idxStr, devStr := "", "0"
	part := strings.SplitN(dev, ",", 2)
	if len(part) == 1 {
		idxStr = part[0]
	} else {
		idxStr, devStr = part[0], part[1]
	}
	cardIdx := idxStr
	if _, err := strconv.Atoi(idxStr); err != nil {
		cardIdx = findCardByAlias(idxStr)
		if cardIdx == "" {
			return 0, 0, 0, false
		}
	}
	if _, err := strconv.Atoi(devStr); err != nil {
		return 0, 0, 0, false
	}
	p := fmt.Sprintf("/proc/asound/card%s/pcm%sp/sub0/hw_params", cardIdx, devStr)
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, 0, 0, false
	}
	var format string
	for _, ln := range strings.Split(string(b), "\n") {
		if m := hwFileRe.FindStringSubmatch(ln); m != nil {
			format = m[1]
		}
		if m := hwRateRe.FindStringSubmatch(ln); m != nil {
			rate, _ = strconv.Atoi(m[1])
		}
		if m := hwChRe.FindStringSubmatch(ln); m != nil {
			ch, _ = strconv.Atoi(m[1])
		}
	}
	bits = formatBits(format)
	return rate, bits, ch, format != "" && rate > 0
}

var (
	hwFileRe  = regexp.MustCompile(`^format:\s*(\S+)`)
	hwRateRe  = regexp.MustCompile(`^rate:\s*(\d+)`)
	hwChRe    = regexp.MustCompile(`^channels:\s*(\d+)`)
	cardBrace = regexp.MustCompile(`\[\s*([A-Za-z0-9_-]+)\s*\]`)
)

var fmtBits = map[string]int{
	"S8": 8, "U8": 8,
	"S16_LE": 16, "S16_BE": 16,
	"S24_LE": 24, "S24_BE": 24, "S24_3LE": 24, "S24_3BE": 24,
	"S32_LE": 32, "S32_BE": 32,
}

// formatBits 把 ALSA 格式串换算成容器位深;未知给 -1 便于报错。
func formatBits(format string) int {
	if b, ok := fmtBits[strings.TrimSpace(format)]; ok {
		return b
	}
	return -1
}

func findCardByAlias(alias string) string {
	b, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		m := cardBrace.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		if m[1] == alias {
			fs := strings.Fields(ln)
			if len(fs) > 0 {
				return fs[0]
			}
		}
	}
	return ""
}
