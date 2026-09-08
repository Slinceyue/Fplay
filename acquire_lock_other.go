//go:build !linux

package main

// acquireLockPlatform 其它平台目前是 no-op(Windows 可用 LockFileEx,以后补)。
// 注意:这意味着 Windows 上可以多开;实际部署到 Windows 之前补上。
func acquireLockPlatform() func() { return func() {} }
