package main

import (
	"strings"
	"testing"
)

func TestRuneWidth(t *testing.T) {
	// ASCII/半角 = 1,中日韩全角 = 2
	cases := []struct {
		r rune
		w int
	}{
		{'a', 1},
		{'1', 1},
		{' ', 1},
		{'中', 2},
		{'文', 2},
	}
	for _, c := range cases {
		if got := runeW(c.r); got != c.w {
			t.Errorf("runeW(%q) = %d, want %d", c.r, got, c.w)
		}
	}
}

func TestDispW(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"中文", 4},
		{"a中b", 4},
	}
	for _, c := range cases {
		if got := dispW(c.s); got != c.want {
			t.Errorf("dispW(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestClipW(t *testing.T) {
	// 短文本:不截断原样返回
	if got := clipW("hello", 10); got != "hello" {
		t.Errorf("clipW short = %q", got)
	}
	// 长文本:截断到 width,末尾带省略号,且显示宽 <= width
	s := "一二三四五六七八九十"
	got := clipW(s, 5)
	if d := dispW(got); d > 5 {
		t.Errorf("clipW too wide: dispW(%q)=%d > 5", got, d)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clipW should end with …: %q", got)
	}
	// 0/负宽返回空
	if clipW("abc", 0) != "" {
		t.Error("clipW width<=0 should be empty")
	}
}

func TestPadTo(t *testing.T) {
	if got := padTo("ab", 5); dispW(got) != 5 || got[:2] != "ab" {
		t.Errorf("padTo: %q", got)
	}
	// 已有宽度 ≥ 目标:原样返回
	if got := padTo("abcdef", 4); got != "abcdef" {
		t.Errorf("padTo long: %q", got)
	}
}
