//go:build !linux

// 非 Linux 没有 /dev/input 媒体键;mediaListener 直接退出。
package main

func ensureMediaKeyAccess() {}
func mediaListener(ch chan<- mkey) {
	_ = ch
}
