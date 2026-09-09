//go:build !linux && !windows

package main

// acquireLockPlatform 其它平台目前是 no-op(Windows 见 acquire_lock_windows.go)。
func acquireLockPlatform() func() { return func() {} }
