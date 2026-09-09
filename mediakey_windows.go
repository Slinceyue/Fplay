//go:build windows

package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 键盘多媒体键(键盘 MPRIS-style):用 user32 RegisterHotKey 注册,WM_HOTKEY 消息泵取。
var (
	user32RegisterHotKey = windows.NewLazySystemDLL("user32.dll").NewProc("RegisterHotKey")
	user32GetMessageW    = windows.NewLazySystemDLL("user32.dll").NewProc("GetMessageW")
	user32DispatchMsgW   = windows.NewLazySystemDLL("user32.dll").NewProc("DispatchMessageW")
)

// 媒体键虚拟键码(winuser.h)。
const (
	vkMediaPlayPause = 0xB3
	vkMediaNextTrack = 0xB0
	vkMediaPrevTrack = 0xB1
	vkVolumeMute     = 0xAD
	vkVolumeDown     = 0xAE
	vkVolumeUp       = 0xAF
	modNoRepeat      = 0x4000 // MOD_NOREPEAT
	msgHotKey        = 0x0312 // WM_HOTKEY
)

// ensureMediaKeyAccess Windows 无需事先改权限(媒体键走系统热键,不涉及设备文件)。
func ensureMediaKeyAccess() {}

// msg 是 Win32 MSG 的 64 位布局(Go 会自动按对齐填充)。
type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
}

// mediaListener 注册键盘多媒体键,并在独立线程泵 WM_HOTKEY 转发到 ch。
// RegisterHotKey/GetMessage 必须在同一 OS 线程跑,故 LockOSThread 钉住。
func mediaListener(ch chan<- mkey) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hotkeys := []struct {
		id int
		vk uintptr
		mk mkey
	}{
		{1, vkMediaPlayPause, mPlayPause},
		{2, vkVolumeUp, mVolUp},
		{3, vkVolumeDown, mVolDown},
		{4, vkVolumeMute, mMute},
		{5, vkMediaNextTrack, mNext},
		{6, vkMediaPrevTrack, mPrev},
	}
	byID := make(map[uintptr]mkey, len(hotkeys))
	for _, h := range hotkeys {
		// RegisterHotKey(NULL, id, MOD_NOREPEAT, vk):hwnd 传 0 → 注册到本线程。
		r, _, _ := user32RegisterHotKey.Call(0, uintptr(h.id), modNoRepeat, h.vk)
		if r != 0 {
			byID[uintptr(h.id)] = h.mk
		}
	}

	var m msg
	for {
		// GetMessageW(MSG*, NULL, 0, 0):返回 0=WM_QUIT,-1=错误;阻塞等待消息。
		r, _, _ := user32GetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if m.message == msgHotKey {
			if mk, ok := byID[m.wParam]; ok {
				select {
				case ch <- mk:
				default: // 主循环忙则丢,不阻塞
				}
			}
		}
		user32DispatchMsgW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
