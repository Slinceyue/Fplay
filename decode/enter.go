package decode

import (
	"io"
	"os"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
)

type Format struct {
	SampleRate    int    // Hz
	Channels      int    // 声道数
	BitsPerSample int    // 每样本位数:8/16/24/32
	NSamples      uint64 // 每声道总样本数(0 = 未知)
}

type Decoder struct {
	path   string
	stream *flac.Stream
	file   *os.File
	done   bool
}

func NewDecoder(path string) (*Decoder, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stream, err := flac.NewSeek(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	// 挂回磁盘缓存的 SeekTable(跨进程/跨播放复用)。
	// 注意:这里绝不后台建表 —— 那会和正在解码的流抢同一 reader 导致崩溃。
	// 首次真正需要跳转时,由引擎单线程懒建并落盘(见 SeekSample)。
	_ = seekCache.ApplyToStream(stream, path)
	return &Decoder{path: path, stream: stream, file: f}, nil
}

func (d *Decoder) Format() *Format {
	info := d.stream.Info
	return &Format{
		SampleRate:    int(info.SampleRate),
		Channels:      int(info.NChannels),
		BitsPerSample: int(info.BitsPerSample),
		NSamples:      info.NSamples,
	}
}

func (d *Decoder) Duration() time.Duration {
	return time.Duration(d.stream.Info.NSamples) * time.Second / time.Duration(d.stream.Info.SampleRate)
}

// SeekSample 跳到包含 sample 的那一帧开头,返回实际起始采样号(<=sample)。
// 只在"播放协程单线程内"调用(engine 的 runTrack),不与 Next 并发。
func (d *Decoder) SeekSample(sample int64) (int64, error) {
	if sample < 0 {
		sample = 0
	}
	pos, err := d.stream.Seek(uint64(sample))
	if err != nil {
		return 0, err
	}
	// 首次 Seek 会让库内部建表,这里顺手落盘,下次直接复用磁盘缓存。
	d.saveSeekTableIfAny()
	return int64(pos), nil
}

// saveSeekTableIfAny 把流内部已建好的 SeekTable 反射出来落盘;失败静默。
func (d *Decoder) saveSeekTableIfAny() {
	if d.stream == nil || d.stream.Info == nil {
		return
	}
	st := streamSeekTable(d.stream)
	if st != nil {
		seekCache.Save(d.path, st, d.stream.Info)
	}
}

func (d *Decoder) Next() (*frame.Frame, error) {
	// ParseNext 解析整帧含音频样本;Next 只解析帧头,Samples 会是空的。
	f, err := d.stream.ParseNext()
	if err == io.EOF {
		d.done = true
	}
	return f, err
}

func (d *Decoder) Done() bool { return d.done }

func (d *Decoder) Close() error {
	if d.stream != nil {
		// 同步退出:让库有机会写出 seekTable(如有)
		_ = d.stream.Close()
	}
	if d.file != nil {
		_ = d.file.Close()
	}
	return nil
}
