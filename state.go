package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry 是文件列表里的一个可选项:子目录或音频文件。
type Entry struct {
	Name string
	Path string
	Dir  bool
}

func stateFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".flacplayer-state.json"
	}
	return filepath.Join(home, ".config", "flacplayer", "state.json")
}

// State 需要跨会话记住的东西。
type State struct {
	Folder  string `json:"folder"`
	Mode    string `json:"mode"`
	Device  string `json:"device"`  // ALSA 输出设备,如 hw:1,0
	Current string `json:"current"` // 最近播放/正在播的歌曲
	Volume  int    `json:"volume"`  // 上次音量 0..150(0=未保存,用默认)
}

func loadState() State {
	var s State
	b, err := os.ReadFile(stateFile())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func saveState(s State) {
	p := stateFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(p, b, 0o644)
}

// listDir 列出目录:先是 ".."(上级),再子目录,再音频文件,各按名排序。
func listDir(dir string) ([]Entry, error) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, errors.New("不是有效目录: " + dir)
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var dirs, files []Entry
	for _, de := range des {
		nm := de.Name()
		if strings.HasPrefix(nm, ".") {
			continue
		}
		if de.IsDir() {
			dirs = append(dirs, Entry{Name: nm + "/", Path: filepath.Join(dir, nm), Dir: true})
		} else if isAudio(nm) {
			files = append(files, Entry{Name: nm, Path: filepath.Join(dir, nm), Dir: false})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	out := make([]Entry, 0, len(dirs)+len(files)+1)
	parent := filepath.Dir(dir)
	if parent != dir {
		out = append(out, Entry{Name: "..", Path: parent, Dir: true})
	}
	out = append(out, dirs...)
	out = append(out, files...)
	return out, nil
}

func isAudio(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".flac")
}

// filesIn 返回当前列表里纯音频文件的路径数组(与展示顺序一致)。
func filesIn(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		if !e.Dir {
			out = append(out, e.Path)
		}
	}
	return out
}
