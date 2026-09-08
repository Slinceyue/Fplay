package decode

import (
	"io"
	"os"
	"reflect"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
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
	// 优先挂回缓存的 SeekTable(跨进程/跨播放复用),让大跨度快进瞬时。
	if seekCache.ApplyToStream(stream, path) {
		return &Decoder{path: path, stream: stream, file: f}, nil
	}
	// 没缓存:后台扫一遍建表 + 落盘(用户继续播放,不阻塞)。
	d := &Decoder{path: path, stream: stream, file: f}
	go d.buildAndCacheSeekTable()
	return d, nil
}

// buildAndCacheSeekTable 触发库扫描整首建表,然后把表落盘到磁盘。
func (d *Decoder) buildAndCacheSeekTable() {
	s := d.stream
	if s == nil || s.Info == nil {
		return
	}
	// Seek 到中点会触发 makeSeekTable(库自己扫整首)
	if _, err := s.Seek(s.Info.NSamples / 2); err != nil {
		return
	}
	// 拉一下内部的 seekTable 私有字段
	v := reflect.ValueOf(s).Elem().FieldByName("seekTable")
	if !v.IsValid() {
		return
	}
	if v.IsNil() {
		return
	}
	if st, ok := v.Interface().(*meta.SeekTable); ok && st != nil {
		seekCache.Save(d.path, st, s.Info)
	}
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
func (d *Decoder) SeekSample(sample int64) (int64, error) {
	if sample < 0 {
		sample = 0
	}
	pos, err := d.stream.Seek(uint64(sample))
	if err != nil {
		return 0, err
	}
	return int64(pos), nil
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
