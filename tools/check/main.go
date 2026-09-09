// check 对比 FLAC 文件的位深/采样率 与 当前正在播的音频设备实际格式,
// 判断是否逐位直出(有没有被重采样/降位)。
//
//	./flaccheck -flac 歌.flac [-dev <设备:linux=hw:1,0 / windows=端点ID>]
//
// 不加 -dev 时只打印文件信息。退出码:0=一致直出,1=不一致/读不到。
// 设备格式按平台取(见 dev_linux.go / dev_windows.go)。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"FlacPlayer/decode"
)

func main() {
	flacPath := flag.String("flac", "", "FLAC 文件路径")
	dev := flag.String("dev", "", "正在播放的音频设备(linux=hw:1,0 / windows=端点ID)")
	flag.Parse()

	if *flacPath == "" {
		// 没给 -flac:直接从 state.json 取正在播的歌与设备。
		// 这样 Windows 上就不用把(可能是中文的)路径经 PowerShell 传参,避免编码错乱。
		if p, d, ok := stateSong(); !ok {
			fmt.Fprintln(os.Stderr, "用法: flaccheck -flac 歌.flac [-dev 设备]  (或不传参数,自动从 state.json 读取)")
			os.Exit(2)
		} else {
			*flacPath, *dev = p, d
			fmt.Printf("从 state.json 读取: %s\n  设备: %s\n", *flacPath, *dev)
		}
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
	dr, bits, dc, ok := deviceFormat(*dev)
	if !ok {
		fmt.Printf("设备 %s 当前没有在播(读不到格式);请先开始播放再检测\n", *dev)
		os.Exit(1)
	}
	fmt.Printf("  设备         采样率=%d Hz  位深=%d bit  声道=%d\n", dr, bits, dc)

	// 位深判定:设备用更大容器装(如 24bit 塞进 32bit 容器)算直出。
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
			fmt.Printf("  位深不一致: 文件 %d vs 设备 %d\n", f.BitsPerSample, bits)
		}
	}
	if !pass {
		os.Exit(1)
	}
}

// stateSong 从 ~/.config/flacplayer/state.json 取正在播的歌和输出设备。
// 不传 -flac 时用;让调用方无需经命令行传路径(规避中文文件名在 Windows 编码错乱)。
func stateSong() (path, dev string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	b, err := os.ReadFile(filepath.Join(home, ".config", "flacplayer", "state.json"))
	if err != nil {
		return "", "", false
	}
	var s struct {
		Current string `json:"current"`
		Device  string `json:"device"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", "", false
	}
	return s.Current, s.Device, s.Current != ""
}
