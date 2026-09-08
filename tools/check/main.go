// check 对比 FLAC 文件的位深/采样率 与 当前正在播的 ALSA 设备实际格式,
// 判断是否逐位直出(有没有被重采样/降位)。
//
//	./flaccheck -flac 歌.flac [-dev hw:1,0]
//
// 不加 -dev 时只打印文件信息。退出码:0=一致直出,1=不一致/读不到。
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"FlacPlayer/decode"
)

func main() {
	flacPath := flag.String("flac", "", "FLAC 文件路径")
	dev := flag.String("dev", "", "正在播放的 ALSA 设备,如 hw:1,0 / hw:CARD=ECHOA,DEV=0")
	flag.Parse()

	if *flacPath == "" {
		fmt.Fprintln(os.Stderr, "用法: flaccheck -flac 歌.flac [-dev hw:1,0]")
		os.Exit(2)
	}

	dec, err := decode.NewDecoder(*flacPath)
	if err != nil {
		fmt.Println("打开 FLAC 失败:", err)
		os.Exit(1)
	}
	defer dec.Close()
	f := dec.Format()
	fmt.Printf("文件 %s\n", *flacPath)
	fmt.Printf("  STREAMINFO  采样率=%d Hz  位深=%d bit  声道=%d\n", f.SampleRate, f.BitsPerSample, f.Channels)

	if *dev == "" {
		return
	}
	dr, df, dc, ok := readHwParams(*dev)
	if !ok {
		fmt.Printf("设备 %s 当前没有在播(读不到 hw_params);请先开始播放再检测\n", *dev)
		os.Exit(1)
	}
	fmt.Printf("  ALSA 设备   采样率=%d Hz  格式=%s  声道=%d\n", dr, df, dc)

	// 位深判定:设备用更大容器装(如 24bit 塞进 S32_LE)算直出。
	bits := formatBits(df)
	rateOK := dr == f.SampleRate
	bitsOK := bits == f.BitsPerSample || (bits > f.BitsPerSample && bits <= 32 && f.BitsPerSample <= 24)
	pass := rateOK && bitsOK

	if pass {
		fmt.Println("结果: PASS  逐位直出(无重采样/无降位)")
	} else {
		fmt.Println("结果: FAIL  被处理过")
		if !rateOK {
			fmt.Printf("  采样率不一致: 文件 %d vs 设备 %d\n", f.SampleRate, dr)
		}
		if !bitsOK {
			fmt.Printf("  位深不一致: 文件 %d vs 设备 %s(%d)\n", f.BitsPerSample, df, bits)
		}
	}
	if !pass {
		os.Exit(1)
	}
}

var fmtBits = map[string]int{
	"S8": 8, "U8": 8,
	"S16_LE": 16, "S16_BE": 16,
	"S24_LE": 24, "S24_BE": 24, "S24_3LE": 24, "S24_3BE": 24,
	"S32_LE": 32, "S32_BE": 32,
}

func formatBits(format string) int {
	if b, ok := fmtBits[strings.TrimSpace(format)]; ok {
		return b
	}
	// 未知格式给个不可能匹配的负数,方便报错。
	return -1
}

var (
	hwFileRe  = regexp.MustCompile(`^format:\s*(\S+)`)
	hwRateRe  = regexp.MustCompile(`^rate:\s*(\d+)`)
	hwChRe    = regexp.MustCompile(`^channels:\s*(\d+)`)
	cardBrace = regexp.MustCompile(`\[\s*([A-Za-z0-9_-]+)\s*\]`)
)

// readHwParams 从 /proc/asound 读某设备当前运行参数。dev 形如 hw:1,0 或 hw:CARD=ECHOA,DEV=0。
func readHwParams(dev string) (rate int, format string, ch int, ok bool) {
	dev = strings.TrimPrefix(dev, "hw:")
	idxStr, devStr := "", "0"
	part := strings.SplitN(dev, ",", 2)
	if len(part) == 1 {
		idxStr = part[0]
	} else {
		idxStr, devStr = part[0], part[1]
	}
	// 数字卡号直接用;否则查 /proc/asound/cards 的 [] 别名。
	cardIdx := idxStr
	if _, err := strconv.Atoi(idxStr); err != nil {
		cardIdx = findCardByAlias(idxStr)
		if cardIdx == "" {
			return 0, "", 0, false
		}
	}
	if _, err := strconv.Atoi(devStr); err != nil {
		return 0, "", 0, false
	}
	p := fmt.Sprintf("/proc/asound/card%s/pcm%sp/sub0/hw_params", cardIdx, devStr)
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, "", 0, false
	}
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
	return rate, format, ch, format != "" && rate > 0
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
