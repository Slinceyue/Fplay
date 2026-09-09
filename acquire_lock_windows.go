//go:build windows

package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// acquireLockPlatform 用 LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY)
// 实现单实例锁:被其它实例占用(ERROR_LOCK_VIOLATION)时立即返回 nil → 调用方提示退出。
func acquireLockPlatform() func() {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".config", "flacplayer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "player.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}
	// 锁第 0 个字节,范围 1 字节;0x2=独占,0x1=拿不到立即失败(不阻塞)。
	if err := windows.LockFileEx(windows.Handle(f.Fd()), 0x2|0x1, 0, 1, 0, new(windows.Overlapped)); err != nil {
		_ = f.Close()
		return nil // 已有实例在跑
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
		_ = f.Close()
	}
}
