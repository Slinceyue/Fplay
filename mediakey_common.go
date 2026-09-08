// 跨平台的"媒体键"命令类型与常量(具体实现见 mediakey_linux.go / mediakey_other.go)。
package main

// mkey 媒体键命令(来自耳机/键盘的原生按键)。
type mkey int

const (
	mPlayPause mkey = iota
	mVolUp
	mVolDown
	mMute
	mNext
	mPrev
)
