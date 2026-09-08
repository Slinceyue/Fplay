package decode

import (
	"reflect"
	"testing"

	"github.com/mewkiz/flac/meta"
)

func TestSeekTableRoundtrip(t *testing.T) {
	st := &meta.SeekTable{Points: []meta.SeekPoint{
		{SampleNum: 0, Offset: 4096, NSamples: 4608},
		{SampleNum: 4608, Offset: 8192, NSamples: 4608},
		{SampleNum: 9216, Offset: 12800, NSamples: 4096},
	}}
	data := encodeSeekTable(st)
	if data == nil {
		t.Fatal("encode returned nil")
	}
	got := decodeSeekTable(data)
	if got == nil {
		t.Fatal("decode returned nil")
	}
	if !reflect.DeepEqual(got.Points, st.Points) {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got.Points, st.Points)
	}
}

func TestSeekTableBadData(t *testing.T) {
	// 垃圾数据 → 解码返回 nil,不 panic
	if decodeSeekTable(nil) != nil {
		t.Error("nil data should decode to nil")
	}
	if decodeSeekTable([]byte{1, 2, 3}) != nil {
		t.Error("truncated data should decode to nil")
	}
	if decodeSeekTable([]byte{0, 0, 0, 5, 1}) != nil { // 声称 5 点但长度不够
		t.Error("length-mismatch data should decode to nil")
	}
}
