//go:build windows

package main

import "testing"

// 单实例锁功能验证:第一次拿到锁,第二次(同进程另一个句柄)被拒。
// 若已有一个 Fplay 实例在跑(锁被占),说明单实例生效,跳过而非失败。
func TestAcquireLockWindows(t *testing.T) {
	unlock1 := acquireLockPlatform()
	if unlock1 == nil {
		t.Skip("已有 Fplay 实例占用单实例锁——锁工作正常,跳过")
	}
	defer unlock1()
	if unlock2 := acquireLockPlatform(); unlock2 != nil {
		unlock2()
		t.Fatal("第二次获取锁应失败(单实例)")
	}
	t.Log("单实例锁工作正常:第二次获取被拒")
}
