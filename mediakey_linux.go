//go:build linux

package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// ensureMediaKeyAccess 检查 /dev/input/event* 是否可读;不可读就用 setfacl 补权限。
// 不可写 / 没 sudo / 出错都静默返回(只是没媒体键控制,不影响其它功能)。
func ensureMediaKeyAccess() {
	// 任一 event 文件不可读就需要补权限(通常 user 不在 input 组)
	des, err := os.ReadDir("/dev/input")
	if err != nil {
		return
	}
	needFix := false
	for _, de := range des {
		if !strings.HasPrefix(de.Name(), "event") {
			continue
		}
		p := filepath.Join("/dev/input", de.Name())
		if f, err := os.Open(p); err != nil {
			needFix = true
			break
		} else {
			_ = f.Close()
		}
	}
	if !needFix {
		return
	}
	// 用 pkexec(图形)或 sudo 调 setfacl
	me, _ := userCurrent()
	rule := "u:" + me + ":rw"
	args := []string{"-m", rule}
	for _, de := range des {
		if strings.HasPrefix(de.Name(), "event") {
			args = append(args, filepath.Join("/dev/input", de.Name()))
		}
	}
	if _, err := runSudo("setfacl", args); err == nil {
		// 设好后清掉旧 listen 状态;让 mediaListener 重新扫描设备
	}
}

// userCurrent 当前用户名(优先 env,再退到 id -un)。
func userCurrent() (string, error) {
	if u := os.Getenv("USER"); u != "" {
		return u, nil
	}
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runSudo 通过 sudo 或 pkexec 跑命令,获取管理员权限。
func runSudo(prog string, args []string) ([]byte, error) {
	if path, _ := exec.LookPath("pkexec"); path != "" && os.Getenv("DISPLAY") != "" {
		return exec.Command(path, append([]string{prog}, args...)...).CombinedOutput()
	}
	return exec.Command("sudo", append([]string{prog}, args...)...).CombinedOutput()
}

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
