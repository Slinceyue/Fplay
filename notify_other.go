//go:build !linux

// 非 Linux(如 Windows)无通知栏常驻卡片,给空实现保证可编译、后续跨平台。
package main

type notifier struct{}

func newNotifier(ch chan<- mkey) *notifier { return &notifier{} }

func (n *notifier) update(path string, playing, paused bool) {}
