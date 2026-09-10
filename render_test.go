package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// 回归:进度条填充必须单调不减,且整行宽度恒定(否则条右缘/百分比会左右跳)。
func TestProgressBarStable(t *testing.T) {
	a := &app{ps: &playState{}}
	a.ps.SetVol(50)
	atomic.StoreInt64(&a.ps.rate, 192000)
	atomic.StoreInt64(&a.ps.total, 2207427)
	atomic.StoreInt32(&a.ps.playing, 1)
	a.current = "x.flac"

	var prevFill = -1
	var prevRunes = -1
	type step struct{ p float64 }
	for _, st := range []step{{0}, {0.05}, {0.1}, {0.25}, {0.5}, {0.75}, {0.9}, {0.97}, {0.98}, {0.99}, {0.995}, {1}} {
		atomic.StoreInt64(&a.ps.pos, int64(st.p*2207427))
		row := a.progressRow(80)
		fill := countBlocks(row)
		runes := len([]rune(row))
		if fill < prevFill {
			t.Fatalf("pct=%v: 条填充变少 %d -> %d (条右缘回退)", st.p*100, prevFill, fill)
		}
		if prevRunes >= 0 && runes != prevRunes {
			t.Fatalf("pct=%v: 整行宽度变化 %d -> %d (条/百分比左右跳)", st.p*100, prevRunes, runes)
		}
		prevFill, prevRunes = fill, runes
	}
}

// countBlocks 数一行里进度条方块/部分块字符个数(即"填充量")。
func countBlocks(s string) int {
	const set = "▏▎▍▌▋▊█"
	n := 0
	for _, r := range s {
		if strings.ContainsRune(set, r) {
			n++
		}
	}
	return n
}

func TestQualText(t *testing.T) {
	cases := []struct {
		name             string
		rate, bits       int64
		excl, bitExact   int32
		devRate, devBits int64
		want             string
	}{
		{"独占 48k/24", 48000, 24, 1, 1, 48000, 24, "独占 直出 48k/24bit"},
		{"共享匹配 48k/24", 48000, 24, 0, 1, 48000, 32, "共享 直出 48k/24bit"},
		{"共享重采样 96k/24→48k/32", 96000, 24, 0, 0, 48000, 32, "共享 重采样 96k/24bit→48k/32bit"},
		{"共享不匹配 44.1k/16→48k/32", 44100, 16, 0, 0, 48000, 32, "共享 重采样 44.1k/16bit→48k/32bit"},
	}
	for _, c := range cases {
		a := &app{ps: &playState{}}
		atomic.StoreInt64(&a.ps.rate, c.rate)
		atomic.StoreInt32(&a.ps.bits, int32(c.bits))
		atomic.StoreInt32(&a.ps.exclusive, c.excl)
		atomic.StoreInt32(&a.ps.bitExact, c.bitExact)
		atomic.StoreInt64(&a.ps.devRate, c.devRate)
		atomic.StoreInt32(&a.ps.devBits, int32(c.devBits))
		got := a.qualText()
		t.Logf("%-28s -> %q", c.name, got)
		if got != c.want {
			t.Errorf("qualText() = %q, want %q", got, c.want)
		}
	}
}
