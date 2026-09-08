package main

import (
	"os"
	"unicode/utf8"

	"golang.org/x/term"
)

type keyKind int

const (
	keyChar keyKind = iota // 可打印字符,ch 有效
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyEsc
	keyBack
	keyHome
	keyEnd
	keyPgUp
	keyPgDn
)

type key struct {
	kind keyKind
	ch   rune
}

// enableRaw 开启原始终端(关回显),返回还原函数。
func enableRaw() (func(), error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, old) }, nil
}

// keyLoop 读取原始终端输入并归一化成 key 发到 ch。
func keyLoop(ch chan<- key) {
	buf := make([]byte, 1)
	var pending []byte // utf-8 累积
	var mouseBuf []byte
	state := 0     // 0 普通,1 见 ESC,2 见 ESC [,3 见 ESC [ 数字 ~,9 见 ESC[M 鼠标
	seq := byte(0) // ESC [ 后面的数字(1/4/5/6)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 || err != nil {
			return
		}
		b := buf[0]

		switch state {
		case 1: // 刚收到 ESC
			if b == '[' {
				state = 2
			} else {
				state = 0
				ch <- key{kind: keyEsc}
			}
			continue
		case 2: // ESC [
			state = 0
			switch b {
			case 'A':
				ch <- key{kind: keyUp}
			case 'B':
				ch <- key{kind: keyDown}
			case 'C':
				ch <- key{kind: keyRight}
			case 'D':
				ch <- key{kind: keyLeft}
			case 'H':
				ch <- key{kind: keyHome}
			case 'F':
				ch <- key{kind: keyEnd}
			case '1', '4', '5', '6':
				seq = b
				state = 3
			case 'M': // X10 鼠标事件开头:ESC[M <b+32> <x> <y>
				mouseBuf = mouseBuf[:0]
				state = 9
			}
			continue
		case 3: // ESC [ 数字 ~ (如 ESC[5~ = PageUp);数字 1 也可能是修饰箭头(ESC[1;5A)
			if b == '~' {
				state = 0
				switch seq {
				case '1':
					ch <- key{kind: keyHome}
				case '4':
					ch <- key{kind: keyEnd}
				case '5':
					ch <- key{kind: keyPgUp}
				case '6':
					ch <- key{kind: keyPgDn}
				}
			} else if b == ';' && seq == '1' {
				state = 7 // 修饰键序列:ESC[1;5A 之类
			} else {
				state = 0
			}
			continue
		case 7: // ESC [ 1 ; (等修饰符数字)
			if b == '5' { // Ctrl
				state = 8
			} else {
				state = 0
			}
			continue
		case 8: // ESC [ 1 ; 5 <字母>
			state = 0
			switch b {
			case 'A':
				ch <- key{kind: keyUp}
			case 'B':
				ch <- key{kind: keyDown}
			case 'C':
				ch <- key{kind: keyRight}
			case 'D':
				ch <- key{kind: keyLeft}
			}
			continue
		case 9: // X10 鼠标事件:ESC[M <b+32> <x> <y>
			mouseBuf = append(mouseBuf, b)
			if len(mouseBuf) >= 3 {
				btn := mouseBuf[0]
				mouseBuf = mouseBuf[:0]
				state = 0
				switch btn {
				case 96: // 滚轮上(64+32)
					ch <- key{kind: keyUp}
				case 97: // 滚轮下(65+32)
					ch <- key{kind: keyDown}
				}
			}
			continue
		}

		switch b {
		case 0x1b:
			state = 1
			continue
		case 0x0d, 0x0a:
			ch <- key{kind: keyEnter}
			continue
		case 0x7f, 0x08:
			ch <- key{kind: keyBack}
			continue
		}

		// 普通 ASCII 直接发;高位字节按 UTF-8 拼成 rune 再发。
		if b < utf8.RuneSelf {
			ch <- key{kind: keyChar, ch: rune(b)}
			continue
		}
		pending = append(pending, b)
		r, size := utf8.DecodeRune(pending)
		if r != utf8.RuneError || size > 0 {
			pending = pending[size:]
			if size > 0 {
				ch <- key{kind: keyChar, ch: r}
			}
		}
	}
}

// promptPath 交互式输入路径:回车确认,Backspace 删除,Esc 取消。
func promptPath(ch <-chan key, prompt string) string {
	wr := func(s string) { _, _ = os.Stdout.WriteString(s) }
	wr(prompt)
	var s []rune
	for {
		k := <-ch
		switch k.kind {
		case keyChar:
			s = append(s, k.ch)
			wr(string(k.ch))
		case keyBack:
			if len(s) > 0 {
				s = s[:len(s)-1]
				wr("\b \b")
			}
		case keyEnter:
			wr("\r\n")
			return string(s)
		case keyEsc:
			wr("\r\n")
			return ""
		}
	}
}
