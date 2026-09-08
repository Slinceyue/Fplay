package main

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"FlacPlayer/decode"
	"FlacPlayer/player"
)

// 播放模式。
const (
	modeSeq    = "顺序"
	modeRand   = "随机"
	modeRepeat = "单曲循环"
	modeSingle = "播放单曲"
)

// playState 由引擎更新、UI 读取;原子字段避免竞态。
type playState struct {
	playing int32 // 1=当前有曲在播(含暂停)
	paused  int32
	cancel  int32
	pos     int64 // 已写出的每声道样本数(进度/歌词)
	total   int64 // 总样本数(每声道);0 未知
	rate    int64 // 采样率
	vol     int32 // 软件音量 0..100
}

func (s *playState) Playing() bool { return atomic.LoadInt32(&s.playing) == 1 }
func (s *playState) Paused() bool  { return atomic.LoadInt32(&s.paused) == 1 }
func (s *playState) SetPaused(v bool) {
	if v {
		atomic.StoreInt32(&s.paused, 1)
	} else {
		atomic.StoreInt32(&s.paused, 0)
	}
}
func (s *playState) Pos() int64   { return atomic.LoadInt64(&s.pos) }
func (s *playState) Total() int64 { return atomic.LoadInt64(&s.total) }
func (s *playState) Rate() int64  { return atomic.LoadInt64(&s.rate) }
func (s *playState) Vol() int32   { return atomic.LoadInt32(&s.vol) }
func (s *playState) SetVol(p int32) {
	if p < 0 {
		p = 0
	}
	if p > 150 {
		p = 150
	}
	atomic.StoreInt32(&s.vol, p)
}

type ctrlMsg struct {
	path string
	idx  int     // 音频列表下标
	seek float64 // 从第几秒开始播(0=开头)
}

type resMsg struct {
	idx     int
	reason  string // done / err / skip / stop
	errText string
}

type engine struct {
	dev string
	ps  *playState

	in  chan ctrlMsg
	out chan resMsg

	pending int32 // 1 = 有新命令已入队(仅看标志,不消费队列)

	dec     *decode.Decoder // 复用的解码器(Seek 表缓存)
	decPath string
}

func startEngine(dev string, ps *playState) *engine {
	e := &engine{
		dev: dev,
		ps:  ps,
		in:  make(chan ctrlMsg, 1),
		out: make(chan resMsg, 1),
	}
	go e.loop()
	return e
}

// setDevice 运行时更换输出设备(下次播放生效)。
func (e *engine) setDevice(dev string) {
	e.dev = dev
}

// play 请求播放/切歌,不阻塞;队列只留最新一条。
func (e *engine) play(path string, idx int) {
	e.playAt(path, idx, 0)
}

// playAt 同 play,但从 seek 秒开始。
func (e *engine) playAt(path string, idx int, seek float64) {
	atomic.StoreInt32(&e.pending, 1)
	for {
		select {
		case <-e.in:
		default:
			goto sent
		}
	}
sent:
	e.in <- ctrlMsg{path: path, idx: idx, seek: seek}
}

// stop 取消当前(不换曲)。
func (e *engine) stop() { atomic.StoreInt32(&e.ps.cancel, 1) }

func (e *engine) loop() {
	for {
		m := <-e.in
		atomic.StoreInt32(&e.pending, 0)
		atomic.StoreInt32(&e.ps.cancel, 0)
		e.runTrack(m)
	}
}

// abortReason 返回当前应中止的理由("" 表示继续)。
func (e *engine) abortReason() string {
	if atomic.LoadInt32(&e.pending) == 1 {
		return "skip"
	}
	if atomic.LoadInt32(&e.ps.cancel) == 1 {
		return "stop"
	}
	return ""
}

func (e *engine) runTrack(m ctrlMsg) {
	ps := e.ps
	atomic.StoreInt32(&ps.playing, 1)
	atomic.StoreInt64(&ps.total, 0)

	// 复用当前解码器:同一首歌的 Seek 表只建一次,后续拖动/跳转都瞬时。
	opened := false
	if e.decPath != m.path || e.dec == nil {
		if e.dec != nil {
			_ = e.dec.Close()
			e.dec = nil
		}
		nd, err := decode.NewDecoder(m.path)
		if err != nil {
			e.finish(resMsg{idx: m.idx, reason: "err", errText: err.Error()})
			return
		}
		e.dec = nd
		e.decPath = m.path
		opened = true
	}
	dec := e.dec

	f := dec.Format()
	atomic.StoreInt64(&ps.rate, int64(f.SampleRate))
	atomic.StoreInt64(&ps.total, int64(f.NSamples))

	// 尝试的设备名:先按当前设备逐位直出;打不开时再用 plughw 自动转换层
	// (格式/采样率交给系统换算,适合 USB 音响等固定参数的设备)。
	// 声道也按候选逐个试,第一个成功者胜。
	cands := chanCandidates(f.Channels)
	names := []string{e.dev}
	if pn := plugName(e.dev); pn != "" {
		names = append(names, pn)
	}
	var openCh int
	var a *player.ALSA
	var lastErr error
	for _, dev := range names {
		for _, cand := range cands {
			aa, err := player.OpenALSA(dev, f.SampleRate, cand, f.BitsPerSample)
			if err == nil {
				a, openCh = aa, cand
				break
			}
			lastErr = err
		}
		if a != nil {
			break
		}
	}
	if a == nil {
		e.finish(resMsg{
			idx:     m.idx,
			reason:  "err",
			errText: fmt.Sprintf("输出设备 %s 无法打开(已试声道 %v 及 plughw): %v", e.dev, cands, lastErr),
		})
		return
	}
	defer a.Close()

	chunk := f.SampleRate / 100 // ~10ms 一个检查段
	if chunk < 128 {
		chunk = 128
	}
	devicePaused := false

	// 定位起点:有 seek 则跳到目标采样;新开或重头则归零。
	// 复用解码器 → Seek 表建一次,之后每次拖动都瞬时、不再回退。
	if m.seek > 0 {
		target := int64(m.seek * float64(f.SampleRate))
		if total := atomic.LoadInt64(&ps.total); total > 0 && target >= total {
			target = total - 1 // 别落到正好末尾,留一截自然播完
			if target < 0 {
				target = 0
			}
		}
		pos, err := dec.SeekSample(target)
		if err != nil {
			pos = 0 // 跳过头/异常就从头播,不打断
		}
		atomic.StoreInt64(&ps.pos, pos)
	} else if !opened {
		pos, _ := dec.SeekSample(0) // 复用的解码器回到开头
		atomic.StoreInt64(&ps.pos, pos)
	} else {
		atomic.StoreInt64(&ps.pos, 0) // 新开解码器本就在开头
	}

	for {
		fr, err := dec.Next()
		if err == io.EOF {
			e.finish(resMsg{idx: m.idx, reason: "done"})
			return
		}
		if err != nil {
			e.finish(resMsg{idx: m.idx, reason: "err", errText: err.Error()})
			return
		}

		n := len(fr.Subframes[0].Samples)
		for start := 0; start < n; start += chunk {
			// 暂停:先尝试 ALSA 硬件暂停(无间隙);设备不支持则停止喂,
			// 缓冲放完自动静音,恢复时靠 xrun-recover 兜底。
			if atomic.LoadInt32(&ps.paused) == 1 && !devicePaused {
				_ = a.Pause(true)
				devicePaused = true
			}
			for atomic.LoadInt32(&ps.paused) == 1 {
				if r := e.abortReason(); r != "" {
					e.finish(resMsg{idx: m.idx, reason: r})
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
			if devicePaused {
				_ = a.Pause(false)
				devicePaused = false
			}
			if r := e.abortReason(); r != "" {
				e.finish(resMsg{idx: m.idx, reason: r})
				return
			}

			end := start + chunk
			if end > n {
				end = n
			}
			// 文件声道不足 openCh 时重复末声道(单声道→立体声即复制);
			// 文件声道多于 openCh 时取前几个(5.1→立体声取 L/R)。
			fn := len(fr.Subframes)
			buf := make([]byte, 0, (end-start)*openCh*4)
			vol := ps.Vol()
			for i := start; i < end; i++ {
				for c := 0; c < openCh; c++ {
					idx := c
					if idx >= fn {
						idx = fn - 1
					}
					v := fr.Subframes[idx].Samples[i]
					if vol != 100 {
						v = scaleSample(v, vol)
					}
					buf = a.AppendSample(buf, v)
				}
			}
			if _, err := a.Write(buf); err != nil {
				e.finish(resMsg{idx: m.idx, reason: "err", errText: err.Error()})
				return
			}
			atomic.AddInt64(&ps.pos, int64(end-start))
		}
	}
}

func (e *engine) finish(r resMsg) {
	atomic.StoreInt32(&e.ps.playing, 0)
	select {
	case e.out <- r:
	default:
	}
}

// scaleSample 软件音量缩放(int32,vol 0..100;vol<100 只会变小不会溢出)。
func scaleSample(v int32, vol int32) int32 {
	return int32(int64(v) * int64(vol) / 100)
}

// plugName 把 "hw:X,Y" 转成 "plughw:X,Y"(允许系统做格式/采样率换算)。
func plugName(dev string) string {
	if strings.HasPrefix(dev, "plughw:") || strings.HasPrefix(dev, "plug:") {
		return ""
	}
	if strings.HasPrefix(dev, "hw:") {
		return "plughw:" + strings.TrimPrefix(dev, "hw:")
	}
	return ""
}

// chanCandidates 生成尝试的声道数顺序。
// 常见输出都是立体声(≥2):多声道文件尽量按原声道开(真多声道设备),
// 单声道文件先按 2(复制到双声道),只有设备不支持 2 才退回 1。
func chanCandidates(fileCh int) []int {
	c := fileCh
	if c < 1 {
		c = 1
	}
	var order []int
	if c > 2 {
		order = []int{c, 2, 1}
	} else {
		order = []int{2, 1} // 单/双声道文件:优先标准立体声
	}
	var out []int
	for _, x := range order {
		dup := false
		for _, y := range out {
			if x == y {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, x)
		}
	}
	return out
}
