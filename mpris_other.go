//go:build !linux

// 非 Linux 无 MPRIS,空实现。
package main

type mpris struct{}

func newMpris(ch chan<- mkey) *mpris { return nil }

func (m *mpris) update(path string, playing, paused bool) {}
