package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// hotplug 每 3s 看一次挂载点顶层,新增/移除 U盘/SD 时自动重扫库。
func (a *app) hotplug() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	last := mountsSignature()
	for range t.C {
		sig := mountsSignature()
		if sig == last {
			continue
		}
		last = sig
		a.mu.Lock()
		before := len(a.lib)
		a.rescan(a.dirs)
		after := len(a.lib)
		a.emit(map[string]any{"evt": "rescan", "n": after, "changed": after != before})
		a.mu.Unlock()
	}
}

// mountsSignature 取各挂载点现有顶层目录的指纹。
func mountsSignature() string {
	var parts []string
	for _, root := range []string{"/media", "/run/media", "/mnt"} {
		des, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range des {
			parts = append(parts, filepath.Join(root, de.Name()))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}
