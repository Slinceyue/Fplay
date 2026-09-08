package main

import (
	"reflect"
	"testing"
)

func TestChanCandidates(t *testing.T) {
	cases := []struct {
		in   int
		want []int
	}{
		{1, []int{2, 1}},    // 单声道文件优先立体声
		{2, []int{2, 1}},    // 立体声
		{6, []int{6, 2, 1}}, // 多声道 → 原声道/2/1
		{8, []int{8, 2, 1}}, // 8 声道
		{0, []int{2, 1}},    // 0/负按 1 处理
		{-3, []int{2, 1}},
	}
	for _, c := range cases {
		got := chanCandidates(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("chanCandidates(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMmssTenth(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0:00.0"},
		{0.4, "0:00.4"},
		{5.9, "0:05.9"},
		{83.4, "1:23.4"},
		{210.5, "3:30.5"},
		{-3, "0:00.0"}, // 负数归零
	}
	for _, c := range cases {
		if got := mmssTenth(c.in); got != c.want {
			t.Errorf("mmssTenth(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
