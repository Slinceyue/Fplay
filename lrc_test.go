package main

import "testing"

func TestParseLRC(t *testing.T) {
	raw := `[ar:某人]
[ti:某歌]
[00:01.50]第一句
[00:04.00]第二句
[00:07.25]第三句
[00:10.00][00:12.00]重复标签句
`
	ls := parseLRC(raw)
	// 元信息行([ar:]/[ti:])无 mm:ss 会被忽略
	if len(ls) < 5 {
		t.Fatalf("expected >=5 lyric lines, got %d: %v", len(ls), ls)
	}
	first := ls[0]
	if first.T != 1.5 || first.Text != "第一句" {
		t.Errorf("first = %+v", first)
	}
	// 双标签产生两条同文本记录,时间递增
	idx := curLRC(ls, 0)
	if idx != -1 {
		t.Errorf("t=0 应无当前行, got %d", idx)
	}
	idx = curLRC(ls, 2.0)
	if ls[idx].Text != "第一句" {
		t.Errorf("t=2.0 应指向第一句, got %d:%q", idx, ls[idx].Text)
	}
	idx = curLRC(ls, 8.0)
	if ls[idx].Text != "第三句" {
		t.Errorf("t=8.0 应指向第三句, got %d:%q", idx, ls[idx].Text)
	}
}

func TestCurLRC_Bounds(t *testing.T) {
	ls := parseLRC("[00:01.00]a\n[00:02.00]b\n[00:03.00]c\n")
	if curLRC(ls, 1.5) != 0 {
		t.Error("1.5 -> a")
	}
	if curLRC(ls, 2.0) != 1 {
		t.Error("2.0 -> b")
	}
	if curLRC(ls, 99) != 2 {
		t.Error("99 -> c")
	}
	if curLRC(nil, 1) != -1 {
		t.Error("nil -> -1")
	}
}
