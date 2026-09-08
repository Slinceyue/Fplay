//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// acquireLockPlatform 用 flock(LOCK_EX|LOCK_NB)实现单实例锁。
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}
