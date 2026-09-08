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
	// NewSeek:支持按采样数 Seek(做快进/拖动),没有内嵌 SeekTable 时首次 Seek 会先扫一遍建表。
	stream, err := flac.NewSeek(f)
	if err != nil {
		f.Close()
		return nil, err
	}
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
		_ = d.stream.Close()
	}
	if d.file != nil {
		_ = d.file.Close()
	}
	return nil
}
