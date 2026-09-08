//go:build !linux

package main

// audioEnsure / restoreAudio 在非 Linux 平台是 no-op(没 PipeWire 占声卡,ALSA
// 直写也不存在;Windows 等平台以后用 winmm/wasapi 后端时
// audioEnsure 会负责停用其他独占声卡的进程)。
func (a *app) audioEnsure()  { a.audioStopped = true }
func (a *app) restoreAudio() { a.audioStopped = false }
