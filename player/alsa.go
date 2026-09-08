//go:build linux

// ALSA 直写后端:用 purego 动态加载 libasound,打开 hw 设备独占输出,
// 支持按位深选择原生整数格式(S8/S16_LE/S24_LE/S32_LE),逐位直出不重采样。
package player

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

// ALSA 常量(见 /usr/include/alsa/asound.h 或 kernel uapi)。
const (
	sndPCMStreamPlayback      = 0
	sndPCMAccessRWInterleaved = 3

	sndPCMFormatS8    = 0
	sndPCMFormatS16LE = 2
	sndPCMFormatS24LE = 6
	sndPCMFormatS32LE = 10
)

var (
	alsaLoadOnce sync.Once
	alsaLoadErr  error

	snd_strerror                           func(errnum int32) string
	snd_pcm_open                           func(pcm *uintptr, name string, stream, mode int32) int32
	snd_pcm_close                          func(pcm uintptr) int32
	snd_pcm_hw_params_malloc               func(ptr *uintptr) int32
	snd_pcm_hw_params_free                 func(obj uintptr)
	snd_pcm_hw_params_any                  func(pcm, params uintptr) int32
	snd_pcm_hw_params_set_access           func(pcm, params uintptr, access uint32) int32
	snd_pcm_hw_params_set_format           func(pcm, params uintptr, format int32) int32
	snd_pcm_hw_params_set_channels         func(pcm, params uintptr, val uint32) int32
	snd_pcm_hw_params_set_rate             func(pcm, params uintptr, val *uint32, dir int32) int32
	snd_pcm_hw_params_set_rate_near        func(pcm, params uintptr, val *uint32, dir *int32) int32
	snd_pcm_hw_params_get_channels_min     func(pcm, params uintptr, val *uint) int32
	snd_pcm_hw_params_get_channels_max     func(pcm, params uintptr, val *uint) int32
	snd_pcm_hw_params_set_buffer_size_near func(pcm, params uintptr, val *uint) int32
	snd_pcm_hw_params_set_period_size_near func(pcm, params uintptr, val *uint, dir *int32) int32
	snd_pcm_hw_params                      func(pcm, params uintptr) int32
	snd_pcm_writei                         func(pcm uintptr, buf []byte, size uint) int
	snd_pcm_recover                        func(pcm uintptr, err, silent int32) int32
	snd_pcm_drain                          func(pcm uintptr) int32
	snd_pcm_pause                          func(pcm uintptr, enable int32) int32
)

func loadALSA() error {
	var handle uintptr
	var err error
	for _, name := range []string{"libasound.so.2", "libasound.so"} {
		handle, err = purego.Dlopen(name, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err == nil {
			break
		}
	}
	if handle == 0 {
		return fmt.Errorf("player: 加载 libasound 失败: %w", err)
	}

	purego.RegisterLibFunc(&snd_strerror, handle, "snd_strerror")
	purego.RegisterLibFunc(&snd_pcm_open, handle, "snd_pcm_open")
	purego.RegisterLibFunc(&snd_pcm_close, handle, "snd_pcm_close")
	purego.RegisterLibFunc(&snd_pcm_hw_params_malloc, handle, "snd_pcm_hw_params_malloc")
	purego.RegisterLibFunc(&snd_pcm_hw_params_free, handle, "snd_pcm_hw_params_free")
	purego.RegisterLibFunc(&snd_pcm_hw_params_any, handle, "snd_pcm_hw_params_any")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_access, handle, "snd_pcm_hw_params_set_access")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_format, handle, "snd_pcm_hw_params_set_format")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_channels, handle, "snd_pcm_hw_params_set_channels")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_rate, handle, "snd_pcm_hw_params_set_rate")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_rate_near, handle, "snd_pcm_hw_params_set_rate_near")
	purego.RegisterLibFunc(&snd_pcm_hw_params_get_channels_min, handle, "snd_pcm_hw_params_get_channels_min")
	purego.RegisterLibFunc(&snd_pcm_hw_params_get_channels_max, handle, "snd_pcm_hw_params_get_channels_max")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_buffer_size_near, handle, "snd_pcm_hw_params_set_buffer_size_near")
	purego.RegisterLibFunc(&snd_pcm_hw_params_set_period_size_near, handle, "snd_pcm_hw_params_set_period_size_near")
	purego.RegisterLibFunc(&snd_pcm_hw_params, handle, "snd_pcm_hw_params")
	purego.RegisterLibFunc(&snd_pcm_writei, handle, "snd_pcm_writei")
	purego.RegisterLibFunc(&snd_pcm_recover, handle, "snd_pcm_recover")
	purego.RegisterLibFunc(&snd_pcm_drain, handle, "snd_pcm_drain")
	purego.RegisterLibFunc(&snd_pcm_pause, handle, "snd_pcm_pause")
	return nil
}

// ALSA 是一个独占的播放设备(实现 io.Writer:入参为交错整数 PCM 字节)。
type ALSA struct {
	handle   uintptr
	channels int

	bytesPer int  // 每个样本的容器字节数(1/2/4)
	shift    uint // 内容左移位数:24bit 塞进 4 字节容器时 shift=8
	frame    int  // 每帧字节 = channels * bytesPer

	pauseOK int32 // 0 未知,1 支持硬件暂停,-1 不支持
}

// Pause 硬件暂停/继续。设备不支持时返回错误(引擎回退:停喂 + 恢复时 xrun-recover)。
func (a *ALSA) Pause(on bool) error {
	if a.pauseOK == -1 {
		return fmt.Errorf("snd_pcm_pause: 设备不支持")
	}
	v := int32(0)
	if on {
		v = 1
	}
	if e := snd_pcm_pause(a.handle, v); e < 0 {
		a.pauseOK = -1
		return fmt.Errorf("snd_pcm_pause: %s", snd_strerror(e))
	}
	a.pauseOK = 1
	return nil
}

// alsaCandidate 是一种可尝试的设备样本格式。
type alsaCandidate struct {
	format int32
	bytes  int
	shift  uint
}

// alsaCandidates 按源位深给出候选容器:
// 有的 USB DAC(如 ECHO-A)不收 3 字节 S24_LE,只收 4 字节容器,
// 这时退到 S32_LE 容器、内容左对齐(<<8)。
func alsaCandidates(bps int) []alsaCandidate {
	switch bps {
	case 8:
		return []alsaCandidate{{sndPCMFormatS8, 1, 0}}
	case 16:
		return []alsaCandidate{{sndPCMFormatS16LE, 2, 0}, {sndPCMFormatS32LE, 4, 16}}
	case 24:
		// 一律优先 4 字节 S32 容器:所有设备/转换层都认,避免个别设备
		// 对 3 字节 S24 支持不完整导致错位噪音。
		return []alsaCandidate{{sndPCMFormatS32LE, 4, 8}, {sndPCMFormatS24LE, 3, 0}}
	case 32:
		return []alsaCandidate{{sndPCMFormatS32LE, 4, 0}}
	default:
		return nil
	}
}

// OpenALSA 打开 name 指定的 hw PCM(如 "hw:CARD=ECHOA,DEV=0"),
// 按采样率/声道/位深选原生整数容器,不做重采样。设备被占用会返回 EBUSY。
func OpenALSA(name string, sampleRate, channels, bps int) (*ALSA, error) {
	alsaLoadOnce.Do(func() { alsaLoadErr = loadALSA() })
	if alsaLoadErr != nil {
		return nil, alsaLoadErr
	}

	cands := alsaCandidates(bps)
	if len(cands) == 0 {
		return nil, fmt.Errorf("player: 不支持的位深 %d bit(仅 8/16/24/32)", bps)
	}

	var handle uintptr
	if e := snd_pcm_open(&handle, name, sndPCMStreamPlayback, 0); e < 0 {
		return nil, fmt.Errorf("player: 打开 %s 失败: %s", name, snd_strerror(e))
	}
	closeOnErr := func() { snd_pcm_close(handle) }

	var params uintptr
	if e := snd_pcm_hw_params_malloc(&params); e < 0 {
		closeOnErr()
		return nil, fmt.Errorf("player: hw_params_malloc: %s", snd_strerror(e))
	}
	defer snd_pcm_hw_params_free(params)

	step := func(call func() int32, what string) error {
		if e := call(); e < 0 {
			return fmt.Errorf("player: %s 失败: %s", what, snd_strerror(e))
		}
		return nil
	}
	if err := step(func() int32 { return snd_pcm_hw_params_any(handle, params) }, "hw_params_any"); err != nil {
		closeOnErr()
		return nil, err
	}
	if err := step(func() int32 { return snd_pcm_hw_params_set_access(handle, params, sndPCMAccessRWInterleaved) }, "set_access"); err != nil {
		closeOnErr()
		return nil, err
	}

	// 逐个候选试 set_format,第一个成功的采用。
	var chosen *alsaCandidate
	var formatErrs []string
	for i := range cands {
		e := snd_pcm_hw_params_set_format(handle, params, cands[i].format)
		if e == 0 {
			chosen = &cands[i]
			break
		}
		formatErrs = append(formatErrs, fmt.Sprintf("%s", snd_strerror(e)))
	}
	if chosen == nil {
		closeOnErr()
		return nil, fmt.Errorf("player: %d bit 没有可用容器(尝试了 %d 种都失败)", bps, len(cands))
	}

	if err := step(func() int32 { return snd_pcm_hw_params_set_channels(handle, params, uint32(channels)) }, fmt.Sprintf("set_channels(%d)", channels)); err != nil {
		closeOnErr()
		return nil, err
	}
	// 选最近采样率(set_rate_near 会把选中的值写回 rate)。
	// 拿不到要求的就明确报错,绝不让设备悄悄重采样。
	rate := uint32(sampleRate)
	if err := step(func() int32 { return snd_pcm_hw_params_set_rate_near(handle, params, &rate, nil) }, fmt.Sprintf("set_rate_near(%d)", sampleRate)); err != nil {
		closeOnErr()
		return nil, err
	}
	if rate != uint32(sampleRate) {
		closeOnErr()
		return nil, fmt.Errorf("player: 设备不支持 %d Hz(实际只能 %d Hz);不做重采样,放弃", sampleRate, rate)
	}

	buf := uint(max(sampleRate/1000*40, 512)) // ~40ms 缓冲
	per := uint(max(sampleRate/1000*10, 128)) // ~10ms period
	step(func() int32 { return snd_pcm_hw_params_set_buffer_size_near(handle, params, &buf) }, "set_buffer_size_near")
	step(func() int32 { return snd_pcm_hw_params_set_period_size_near(handle, params, &per, nil) }, "set_period_size_near")
	if err := step(func() int32 { return snd_pcm_hw_params(handle, params) }, "hw_params"); err != nil {
		closeOnErr()
		return nil, err
	}

	return &ALSA{
		handle:   handle,
		channels: channels,
		bytesPer: chosen.bytes,
		shift:    chosen.shift,
		frame:    chosen.bytes * channels,
	}, nil
}

// AppendSample 把一个 int32 样本按设备容器编码追加进 buf(小端,内容左对齐)。
func (a *ALSA) AppendSample(buf []byte, v int32) []byte {
	u := uint32(v) << a.shift
	for i := 0; i < a.bytesPer; i++ {
		buf = append(buf, byte(u>>uint(8*i)))
	}
	return buf
}

// Write 阻塞写入交错 PCM,直到全部进入设备缓冲(实现 io.Writer 的节流)。
func (a *ALSA) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p)%a.frame != 0 {
		return 0, fmt.Errorf("player: ALSA 输入不是整帧(每帧 %d 字节): %d", a.frame, len(p))
	}
	totalFrames := len(p) / a.frame
	off := 0
	for off < totalFrames {
		buf := p[off*a.frame:]
		n := snd_pcm_writei(a.handle, buf, uint(totalFrames-off))
		if n < 0 {
			n = int(snd_pcm_recover(a.handle, int32(n), 1))
			if n < 0 {
				return off * a.frame, fmt.Errorf("player: 写设备失败: %s", snd_strerror(int32(n)))
			}
		}
		off += n
	}
	return len(p), nil
}

// Close 先放完缓冲再关闭。
func (a *ALSA) Close() error {
	if a.handle == 0 {
		return nil
	}
	snd_pcm_drain(a.handle)
	snd_pcm_close(a.handle)
	a.handle = 0
	return nil
}

var cardIdxRe = regexp.MustCompile(`^\s*(\d+)\s+\[`)

// FindECHOADevice 在 /proc/asound/cards 里找 ECHO-A/TTGK 声卡,返回 "hw:<index>,0"。
// 找不到返回 "".可用 FP_DEVICE 环境变量覆盖指定设备。
func FindECHOADevice() string {
	if dev := os.Getenv("FP_DEVICE"); dev != "" {
		return dev
	}
	b, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	for i := 0; i < len(lines); i++ {
		m := cardIdxRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		// 卡描述在本行冒号后,型号通常续行(TTGK Technology ECHO-A at ...)。
		block := lines[i]
		if i+1 < len(lines) {
			block += " " + lines[i+1]
		}
		if strings.Contains(block, "ECHO-A") || strings.Contains(block, "TTGK") {
			return fmt.Sprintf("hw:%s,0", m[1])
		}
	}
	return ""
}

// ProbeChannels 打开设备查询其支持的声道数范围(需设备空闲,被占用会 EBUSY)。
func ProbeChannels(name string) (minCh, maxCh int, err error) {
	alsaLoadOnce.Do(func() { alsaLoadErr = loadALSA() })
	if alsaLoadErr != nil {
		return 0, 0, alsaLoadErr
	}
	var handle uintptr
	if e := snd_pcm_open(&handle, name, sndPCMStreamPlayback, 0); e < 0 {
		return 0, 0, fmt.Errorf("player: 打开 %s 失败: %s", name, snd_strerror(e))
	}
	defer snd_pcm_close(handle)

	var params uintptr
	if e := snd_pcm_hw_params_malloc(&params); e < 0 {
		return 0, 0, fmt.Errorf("player: hw_params_malloc: %s", snd_strerror(e))
	}
	defer snd_pcm_hw_params_free(params)
	if e := snd_pcm_hw_params_any(handle, params); e < 0 {
		return 0, 0, fmt.Errorf("player: hw_params_any: %s", snd_strerror(e))
	}
	var lo, hi uint
	if e := snd_pcm_hw_params_get_channels_min(handle, params, &lo); e < 0 {
		return 0, 0, fmt.Errorf("player: get_channels_min: %s", snd_strerror(e))
	}
	if e := snd_pcm_hw_params_get_channels_max(handle, params, &hi); e < 0 {
		return 0, 0, fmt.Errorf("player: get_channels_max: %s", snd_strerror(e))
	}
	return int(lo), int(hi), nil
}

// AlsaDevice 是可供选择的 playback 输出设备。
type AlsaDevice struct {
	Name  string // "hw:<card>,<dev>"
	Label string // 可读描述
}

var pcmPRe = regexp.MustCompile(`^pcm(\d+)p$`)

// AlsaDevices 枚举系统里所有可用的 ALSA playback PCM(来自 /proc/asound),
// Label 用可读的产品名(ECHO-A / HDA Intel PCH / 显卡 HDMI…)而不是卡 ID。
func AlsaDevices() []AlsaDevice {
	des, _ := os.ReadDir("/proc/asound")
	var out []AlsaDevice
	for _, de := range des {
		n := de.Name()
		if !strings.HasPrefix(n, "card") {
			continue
		}
		idxStr := strings.TrimPrefix(n, "card")
		if _, err := strconv.Atoi(idxStr); err != nil {
			continue
		}
		cardDir := "/proc/asound/" + n
		cardLabel := cardProduct(idxStr)
		pdes, _ := os.ReadDir(cardDir)
		var devs []string
		for _, pd := range pdes {
			if m := pcmPRe.FindStringSubmatch(pd.Name()); m != nil {
				devs = append(devs, m[1])
			}
		}
		multi := len(devs) > 1
		for _, d := range devs {
			label := cardLabel
			if multi {
				if pn := pcmPlayName(idxStr, d); pn != "" {
					label += " · " + pn
				} else {
					label += " · device " + d
				}
			}
			out = append(out, AlsaDevice{
				Name:  "hw:" + idxStr + "," + d,
				Label: label,
			})
		}
	}
	return out
}

// cardProduct 取卡行冒号后的产品名,并加常见的人读注释。
func cardProduct(idx string) string {
	b, err := os.ReadFile("/proc/asound/cards")
	name := ""
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, " "+idx+" ") {
				continue
			}
			if k := strings.Index(line, "]: "); k >= 0 {
				rest := strings.TrimSpace(line[k+3:])
				if i := strings.Index(rest, " - "); i >= 0 {
					name = strings.TrimSpace(rest[i+3:])
				} else {
					name = rest
				}
			}
			break
		}
	}
	if name == "" {
		return "card " + idx
	}
	low := strings.ToLower(name)
	switch {
	case strings.Contains(low, "intel pch"), strings.Contains(low, "realtek"), strings.Contains(low, "alc"):
		return name + "（板载内置）"
	case strings.Contains(low, "nvidia"):
		return name + "（显卡 HDMI）"
	}
	return name
}

// pcmPlayName 读该播放子设备的名字(如 "HDMI 0"),空串表示没有额外区分。
func pcmPlayName(idx, dev string) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/asound/card%s/pcm%sp/info", idx, dev))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "name: ") {
			s := strings.TrimSpace(strings.TrimPrefix(line, "name: "))
			if s == "" {
				return ""
			}
			return s
		}
	}
	return ""
}
