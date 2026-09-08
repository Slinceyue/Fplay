package main

import "fmt"

// mmssTenth 01:23.4 形式的时长(带 0.1s 精度)。
func mmssTenth(v float64) string {
	if v < 0 {
		v = 0
	}
	m := int(v) / 60
	s := int(v) % 60
	d := int((v - float64(int(v))) * 10)
	return fmt.Sprintf("%d:%02d.%d", m, s, d)
}
