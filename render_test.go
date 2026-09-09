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
