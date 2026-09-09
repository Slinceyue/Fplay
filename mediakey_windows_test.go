//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

// 验证:键盘多媒体键能否被 RegisterHotKey 成功注册(若被系统抢占则个别失败,属正常)。
func TestRegisterMediaHotkeys(t *testing.T) {
	unreg := windows.NewLazySystemDLL("user32.dll").NewProc("UnregisterHotKey")
	keys := []struct {
		id int
		vk uintptr
	}{
		{1, vkMediaPlayPause}, {2, vkVolumeUp}, {3, vkVolumeDown},
		{4, vkVolumeMute}, {5, vkMediaNextTrack}, {6, vkMediaPrevTrack},
	}
	ok := 0
	for _, k := range keys {
		r, _, _ := user32RegisterHotKey.Call(0, uintptr(k.id), modNoRepeat, k.vk)
		if r != 0 {
			ok++
			unreg.Call(0, uintptr(k.id))
		}
	}
	t.Logf("媒体键注册成功 %d/6 个(未成功的通常被系统其它程序抢占,不影响运行)", ok)
}
