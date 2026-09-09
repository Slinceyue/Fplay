//go:build windows

package main

import (
	"FlacPlayer/player"
)

// deviceFormat 用 WASAPI GetMixFormat 取端点(或默认)的共享混音格式。
// 共享模式下 WASAPI 最终把数据混到该格式,拿来和 FLAC 比对即可判断有无重采样/降位。
func deviceFormat(dev string) (rate, bits, ch int, ok bool) {
	if dev == "" {
		dev = "default"
	}
	return player.DeviceMixFormat(dev)
}
