package player

import (
	"reflect"
	"testing"
)

// 跨平台:appendPCM 是 Linux(ALSA)/Windows(WASAPI) 共用的样本容器编码,
// 必须保证小端、内容左对齐的逐字节输出与 ALSA 直写一致。
func TestAppendPCM(t *testing.T) {
	tests := []struct {
		name     string
		v        int32
		bytesPer int
		shift    uint
		want     []byte
	}{
		{"8bit", 0x80, 1, 0, []byte{0x80}},
		{"16bit 正", 0x1234, 2, 0, []byte{0x34, 0x12}},
		{"16bit 负(-2)", -2, 2, 0, []byte{0xFE, 0xFF}},
		{"32bit", 0x12345678, 4, 0, []byte{0x78, 0x56, 0x34, 0x12}},
		// 24bit 塞进 4 字节容器:内容左移 8 位(等价 ALSA S32_LE 容器)。
		{"24→32 中值", 0x123456, 4, 8, []byte{0x00, 0x56, 0x34, 0x12}},
		{"24→32 最大", 0x7FFFFF, 4, 8, []byte{0x00, 0xFF, 0xFF, 0x7F}},
		{"24→32 负(-2)", -2, 4, 8, []byte{0x00, 0xFE, 0xFF, 0xFF}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendPCM(nil, tt.v, tt.bytesPer, tt.shift)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("appendPCM(%#x, %d, %d)= %#x, want %#x", tt.v, tt.bytesPer, tt.shift, got, tt.want)
			}
		})
	}
}
