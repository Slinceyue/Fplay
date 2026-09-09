//go:build !linux && !windows

// 非 Linux/Windows 没有 /dev/input 媒体键;mediaListener 直接退出。
package main

func ensureMediaKeyAccess() {}
func mediaListener(ch chan<- mkey) {
	_ = ch
}
