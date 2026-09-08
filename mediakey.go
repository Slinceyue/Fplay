package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 媒体键命令(来自耳机/键盘的原生按键)。
type mkey int

const (
	mPlayPause mkey = iota
	mVolUp
	mVolDown
	mMute
	mNext
	mPrev
)

// evdev 键码(linux/input-event-codes.h)。
const (
	keyVolumeDown   = 114
	keyVolumeUp     = 115
	keyMute         = 113
	keyPlay         = 207
	keyPause        = 119
	keyPlayPause    = 164
	keyNextSong     = 163
	keyPreviousSong = 165
)

// mediaListener 监听所有输入设备的媒体键,转成 mkey 发到 ch。
// 耳机(USB 头戴/遥控)的媒体按钮和键盘多媒体键都会进来。
// 若 /dev/input 无权读取(需属于 input 组),静默跳过。
func mediaListener(ch chan<- mkey) {
	var seen = map[string]bool{}
	stop := map[string]chan struct{}{}

	scan := func() {
		des, err := os.ReadDir("/dev/input")
		if err != nil {
			return
		}
		for _, de := range des {
			if !strings.HasPrefix(de.Name(), "event") {
				continue
			}
			path := filepath.Join("/dev/input", de.Name())
			if seen[path] {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				continue // 权限不足或无此设备
			}
			quit := make(chan struct{})
			seen[path] = true
			stop[path] = quit
			go readMediaEvents(f, quit, ch)
		}
	}
	scan()
	t := time.NewTicker(2 * time.Second) // 设备热插拔时补扫
	defer t.Stop()
	for range t.C {
		scan()
	}
}

// readMediaEvents 阻塞读一个 evdev 设备的事件并过滤媒体键。
func readMediaEvents(f *os.File, quit chan struct{}, ch chan<- mkey) {
	defer f.Close()
	buf := make([]byte, 24) // struct input_event
	for {
		select {
		case <-quit:
			return
		default:
		}
		n, err := f.Read(buf)
		if err != nil || n != len(buf) {
			return // 设备拔出或不可读
		}
		// input_event: sec(int64) usec(int64) type(u16) code(u16) value(i32),小端
		kind := binary.LittleEndian.Uint16(buf[16:18])
		code := binary.LittleEndian.Uint16(buf[18:20])
		val := int32(binary.LittleEndian.Uint32(buf[20:24]))
		if kind != 1 || val != 1 { // 只要 EV_KEY 的按下
			continue
		}
		var mk mkey
		switch code {
		case keyPlayPause, keyPlay, keyPause:
			mk = mPlayPause
		case keyVolumeUp:
			mk = mVolUp
		case keyVolumeDown:
			mk = mVolDown
		case keyMute:
			mk = mMute
		case keyNextSong:
			mk = mNext
		case keyPreviousSong:
			mk = mPrev
		default:
			continue
		}
		select {
		case ch <- mk:
		default: // 主循环忙则丢,不阻塞
		}
	}
}
