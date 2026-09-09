//go:build !linux && !windows

package main

// deviceFormat 在其它平台暂未实现,返回 ok=false。
func deviceFormat(dev string) (rate, bits, ch int, ok bool) {
	_ = dev
	return 0, 0, 0, false
}
