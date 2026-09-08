package decode

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// findTestFlac 从本地环境找一个可用的样例 flac(优先 mewkiz 模块缓存里的 love.flac)。
// 找不到则跳过集成测试,但单元测试不受影响。
func findTestFlac(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	cands := []string{
		filepath.Join(home, "go/pkg/mod/github.com/mewkiz/flac@v1.0.14/testdata/love.flac"),
		filepath.Join(home, "go/pkg/mod/github.com/mewkiz/flac@*/testdata/love.flac"),
		"testdata/love.flac",
	}
	for _, c := range cands {
		if m, _ := filepath.Glob(c); len(m) > 0 {
			for _, p := range m {
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	t.Skip("no sample flac found; run inside GoLand with module cache or set testdata/love.flac")
	return ""
}

func TestDecodeSequentialAndSeek(t *testing.T) {
	p := findTestFlac(t)
	d, err := NewDecoder(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := d.Format()
	if f.SampleRate <= 0 || f.NSamples == 0 {
		t.Fatalf("bad streaminfo: %+v", f)
	}

	// 先顺序解一段
	n1 := 0
	for n1 < 50 {
		if _, err := d.Next(); err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("Next err:", err)
		}
		n1++
	}
	if n1 == 0 {
		t.Fatal("decoded 0 frames")
	}

	// 中途 Seek 到 2/3 再继续解,确保不 panic、进度对
	pos, err := d.SeekSample(int64(f.NSamples * 2 / 3))
	if err != nil {
		t.Fatal("Seek err:", err)
	}
	if pos < 0 || pos > int64(f.NSamples) {
		t.Fatalf("seek pos out of range: %d (nsamples %d)", pos, f.NSamples)
	}
	n2 := 0
	for n2 < 20 {
		if _, err := d.Next(); err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("Next after seek err:", err)
		}
		n2++
	}
	if n2 == 0 {
		t.Error("nothing decoded after seek")
	}
}

// TestDecoderReopenSamedir 两次开关同一文件(验证 Close 不泄漏/不重复建表炸)。
func TestDecoderOpenClose(t *testing.T) {
	p := findTestFlac(t)
	for i := 0; i < 3; i++ {
		d, err := NewDecoder(p)
		if err != nil {
			t.Fatal(err)
		}
		_ = d.Format()
		if _, err := d.SeekSample(int64(0)); err != nil {
			t.Fatal(err)
		}
		_ = d.Close()
	}
}
