//go:build windows

package player

import (
	"testing"
)

func TestBuildCandidates(t *testing.T) {
	tests := []struct {
		name       string
		rate, ch   int
		bps        int
		wantCands  int
		wantBytes  int
		wantShift  uint
		wantValid  uint16
		wantBPS    uint16
		wantChMask uint32
		wantErr    bool
	}{
		{"16bit@44.1k 立体声", 44100, 2, 16, 1, 2, 0, 16, 16, 0x3, false},
		{"24bit:32bit容器优先 + 24bit容器备选", 96000, 2, 24, 2, 4, 8, 24, 32, 0x3, false},
		{"8bit 单声道", 8000, 1, 8, 1, 1, 0, 8, 8, 0x4, false},
		{"32bit 5.1", 48000, 6, 32, 1, 4, 0, 32, 32, 0x3F, false},
		{"不支持的位深", 44100, 2, 7, 0, 0, 0, 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cands, err := buildCandidates(tt.rate, tt.ch, tt.bps)
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望报错,却返回成功")
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if len(cands) != tt.wantCands {
				t.Fatalf("候选数=%d, 期望 %d", len(cands), tt.wantCands)
			}
			c := cands[0]
			if c.bytesPer != tt.wantBytes || c.shift != tt.wantShift {
				t.Fatalf("bytes=%d shift=%d, 期望 bytes=%d shift=%d", c.bytesPer, c.shift, tt.wantBytes, tt.wantShift)
			}
			wfx := c.wfx
			if wfx.wFormatTag != wfmtTagExtensible {
				t.Fatalf("wFormatTag=%#x, 期望 extensible %#x", wfx.wFormatTag, wfmtTagExtensible)
			}
			if wfx.nChannels != uint16(tt.ch) || wfx.nSamplesPerSec != uint32(tt.rate) {
				t.Fatalf("nChannels=%d nSamplesPerSec=%d", wfx.nChannels, wfx.nSamplesPerSec)
			}
			if wfx.samplesValidBits != tt.wantValid || wfx.wBitsPerSample != tt.wantBPS {
				t.Fatalf("valid=%d container=%d, 期望 valid=%d container=%d", wfx.samplesValidBits, wfx.wBitsPerSample, tt.wantValid, tt.wantBPS)
			}
			if wfx.dwChannelMask != tt.wantChMask {
				t.Fatalf("dwChannelMask=%#x, 期望 %#x", wfx.dwChannelMask, tt.wantChMask)
			}
			// 每帧字节 = 声道 * 容器字节;平均字节率 = 采样率 * 帧字节。
			if int(wfx.nBlockAlign) != tt.ch*tt.wantBytes {
				t.Fatalf("nBlockAlign=%d, 期望 %d", wfx.nBlockAlign, tt.ch*tt.wantBytes)
			}
			if int(wfx.nAvgBytesPerSec) != tt.rate*tt.ch*tt.wantBytes {
				t.Fatalf("nAvgBytesPerSec=%d", wfx.nAvgBytesPerSec)
			}
			// 24bit 的备选应是真正的 3 字节 24bit 容器(bytesPer=3, shift=0, 容器=24)。
			if tt.bps == 24 {
				b := cands[1]
				if b.bytesPer != 3 || b.shift != 0 || b.wfx.wBitsPerSample != 24 {
					t.Fatalf("24bit 备选应 3字节/容器24,实为 bytes=%d shift=%d container=%d", b.bytesPer, b.shift, b.wfx.wBitsPerSample)
				}
			}
		})
	}
}

func TestChanMask(t *testing.T) {
	tests := []struct {
		ch   int
		want uint32
	}{
		{1, 0x4}, {2, 0x3}, {3, 0x7}, {4, 0x33}, {5, 0x37}, {6, 0x3F}, {8, 0x3}, {0, 0x3},
	}
	for _, tt := range tests {
		if got := chanMask(tt.ch); got != tt.want {
			t.Errorf("chanMask(%d)=%#x, 期望 %#x", tt.ch, got, tt.want)
		}
	}
}
