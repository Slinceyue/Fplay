// package player 负责把解码出的 PCM 播放出去。
// 后端:ALSA 直写(hw 独占,逐位直出,无重采样)。
package player

import (
	"fmt"
	"io"

	"FlacPlayer/decode"
)

// PlayFile 用 ALSA 直写播放一个已打开的 FLAC 流,播完返回。
// 设备默认自动找 ECHO-A;可用环境变量 FP_DEVICE 指定(如 hw:CARD=ECHOA,DEV=0)。
func PlayFile(dec *decode.Decoder) error {
	f := dec.Format()

	nch := f.Channels
	if nch > 2 {
		nch = 2 // ALSA 这边先按前两声道输出,5.1 以后再支持
	}
	if nch < 1 {
		nch = 1
	}

	device := FindECHOADevice()
	if device == "" {
		return fmt.Errorf("player: 找不到 ECHO-A 声卡;可用 FP_DEVICE=hw:CARD=xxx,DEV=n 环境变量指定输出设备")
	}

	a, err := OpenALSA(device, f.SampleRate, nch, f.BitsPerSample)
	if err != nil {
		return err
	}
	defer a.Close()

	fmt.Printf("   →  %s (%d Hz / %d 声道 / %d bit,ALSA 直写)\n",
		device, f.SampleRate, nch, f.BitsPerSample)

	for {
		fr, err := dec.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		n := len(fr.Subframes[0].Samples)
		buf := make([]byte, 0, n*nch*4) // 容器最多 4 字节 × 声道
		if nch == 1 {
			for i := 0; i < n; i++ {
				buf = a.AppendSample(buf, fr.Subframes[0].Samples[i])
			}
		} else {
			for i := 0; i < n; i++ {
				buf = a.AppendSample(buf, fr.Subframes[0].Samples[i])
				buf = a.AppendSample(buf, fr.Subframes[1].Samples[i])
			}
		}
		if _, err := a.Write(buf); err != nil {
			return err
		}
	}
}
