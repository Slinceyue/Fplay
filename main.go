// Fplay —— 无桌面嵌入式播放器主入口(feature/embedded)。
// 从 stdin 接收一行一个命令,播放状态以 JSON 行输出到 stdout,
// 方便后续接 LCD/触摸屏/Web 等任何前端;也可直接在终端里敲命令用。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"FlacPlayer/player"
)

type track struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`

	lrc []lrcLine // 同名 .lrc(不输出 JSON)
}

type app struct {
	ps  *playState
	eng *engine
	dev string

	audioStopped bool

	mu    sync.Mutex // 序列化所有命令/事件处理
	lib   []track
	curID int // 正在播的库下标;-1 无
	mode  string

	interactive bool
	diag        bool

	dirs []string // 持久化的音乐目录
}

// init 调整 GC:音频输出缓冲很小,播放中一次 GC 停顿就可能喂不上数据而下溢(实测
// GOGC=off 能完全消除)。所以关掉自动 GC,改为在切歌/暂停的安全间隙主动 GC
// (见 engine.runTrack);另设内存软上限做安全阀——万一超长曲目把垃圾攒到上限,
// 运行时仍会兜底 GC,不至于吃爆内存。
func init() {
	debug.SetMemoryLimit(512 << 20)
	debug.SetGCPercent(-1)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	unlock := acquireLockPlatform()
	if unlock == nil {
		return fmt.Errorf("Fplay 已在运行(单实例锁被占),请先退出旧实例")
	}
	defer unlock()

	// 命令行解析:-lib <dir>(可多个)、-diag
	var libs []string
	diag := false
	for i := 0; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-lib":
			if i+1 < len(os.Args) {
				i++
				libs = append(libs, os.Args[i])
			}
		case "-diag":
			diag = true
		case "-h", "--help":
			fmt.Println("用法: fplay [-lib <dir>]... [-diag]")
			return nil
		}
	}

	st := loadState()
	a := &app{curID: -1, mode: modeSeq, diag: diag, dirs: st.Dirs}
	libs = append(libs, a.dirs...)
	if st.Mode != "" {
		a.mode = st.Mode
	}
	a.ps = &playState{}
	a.ps.SetVol(50)
	if st.Volume > 0 {
		a.ps.SetVol(int32(st.Volume))
	}

	// 输出设备
	a.dev = os.Getenv("FP_DEVICE")
	if a.dev == "" {
		a.dev = player.FindECHOADevice()
	}
	if a.dev == "" {
		if ds := player.AlsaDevices(); len(ds) > 0 {
			a.dev = ds[0].Name
		}
	}

	// 信号:退出时恢复系统声音 + 存状态
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer func() {
		a.stop()
		a.save()
	}()
	go func() { // 收到信号即优雅退出(systemd/嵌入式常用)
		<-sig
		a.stop()
		a.save()
		os.Exit(0)
	}()

	// 库扫描(本地 + U盘/SD 挂载的 music);把最终目录去重记回 a.dirs
	a.dirs = uniqStrs(libs)
	a.rescan(a.dirs)

	// 引擎
	a.eng = startEngine(a.dev, a.ps)

	// 媒体键(耳机/按键走 evdev)
	media := make(chan mkey, 16)
	ensureMediaKeyAccess()
	go mediaListener(media)
	go func() {
		for mk := range media {
			a.handleMedia(mk)
		}
	}()

	// 引擎完成/出错通知
	go func() {
		for r := range a.eng.out {
			a.handleRes(r)
		}
	}()

	// 播放进度 JSON 事件(供前端)
	go a.ticker()

	// U盘/SD 热插拔自动重扫
	go a.hotplug()

	// systemd Type=notify:告诉 systemd 我们 READY,并按 WatchdogSec 的一半喂狗。
	sdNotifyReady()
	stopWd := make(chan struct{})
	defer close(stopWd)
	if sdWatchdogEnabled() {
		go sdWatchdogLoop(10*time.Second, stopWd)
	}

	// 交互提示
	a.interactive = term.IsTerminal(int(os.Stdin.Fd()))
	r := bufio.NewReader(os.Stdin)
	a.emit(map[string]any{"evt": "ready", "mode": a.mode, "device": a.dev, "count": len(a.lib)})
	if a.interactive {
		fmt.Fprint(os.Stderr, "fplay> ")
	}
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			// 服务模式(systemd 把 stdin 设 null):EOF 不退出,等信号。
			if !a.interactive {
				select {}
			}
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if a.interactive {
				fmt.Fprint(os.Stderr, "fplay> ")
			}
			continue
		}
		if a.command(line) {
			if a.interactive {
				fmt.Fprint(os.Stderr, "fplay> ")
			}
			continue
		}
		break
	}
	return nil
}

// ---------- 库扫描 ----------

func (a *app) rescan(extra []string) {
	dirs := collectDirs(extra)
	var list []track
	seen := map[string]bool{}
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if strings.HasPrefix(de.Name(), ".") && p != d {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(de.Name()), ".flac") {
				ap, _ := filepath.Abs(p)
				if !seen[ap] {
					seen[ap] = true
					title, artist := readTags(ap)
					if title == "" {
						title = de.Name()
					}
					list = append(list, track{
						ID:     len(list),
						Path:   ap,
						Title:  title,
						Artist: artist,
						lrc:    parseLRCFile(ap),
					})
				}
			}
			return nil
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	for i := range list {
		list[i].ID = i
	}
	a.lib = list
}

// collectDirs: 显式 -lib > env FPLAY_LIB > ~/music > 各挂载点下的 music
func collectDirs(extra []string) []string {
	var out []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" {
			return
		}
		fi, err := os.Stat(d)
		if err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	for _, d := range extra {
		add(d)
	}
	if env := os.Getenv("FPLAY_LIB"); env != "" {
		for _, d := range strings.Split(env, ":") {
			add(d)
		}
	}
	home, _ := os.UserHomeDir()
	add(filepath.Join(home, "music"))
	add(filepath.Join(home, "Music"))
	// 常见可移动介质挂载点下的 music 子目录
	roots := []string{"/media", "/run/media", "/mnt"}
	for _, root := range roots {
		des, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range des {
			mp := filepath.Join(root, de.Name())
			for _, sub := range []string{"music", "Music", "flac", "Flac"} {
				add(filepath.Join(mp, sub))
			}
			add(mp) // 某些盘整盘都是音乐
		}
	}
	return out
}

// ---------- 播放控制(在 a.mu 外调用,内部加锁)----------

func (a *app) handleRes(r resMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch r.reason {
	case "err":
		a.emit(map[string]any{"evt": "error", "text": r.errText})
		if r.idx >= 0 {
			a.emitState()
		}
	case "done":
		a.autoNext()
	case "stop":
		a.emitState()
	}
}

func (a *app) autoNext() {
	if a.curID < 0 || a.curID >= len(a.lib) {
		a.emitState()
		return
	}
	switch a.mode {
	case modeRepeat:
		a.playByID(a.curID, 0)
	case modeSingle:
		a.curID = -1
		a.emitState()
	case modeRand:
		if len(a.lib) > 1 {
			id := a.curID
			for id == a.curID {
				id = int(time.Now().UnixNano() % int64(len(a.lib)))
			}
			a.playByID(id, 0)
		} else {
			a.playByID(a.curID, 0)
		}
	default: // 顺序
		if a.curID+1 < len(a.lib) {
			a.playByID(a.curID+1, 0)
		} else {
			a.curID = -1
			a.emitState()
		}
	}
}

func (a *app) playByID(id int, seekSec float64) {
	if id < 0 || id >= len(a.lib) {
		a.emit(map[string]any{"evt": "error", "text": fmt.Sprintf("库内没有 %d 号(共 %d 首)", id, len(a.lib))})
		return
	}
	a.audioEnsure()
	a.curID = id
	tr := a.lib[id]
	a.eng.playAt(tr.Path, id, seekSec)
	a.save()
	a.emit(map[string]any{
		"evt":    "song",
		"id":     id,
		"title":  tr.Title,
		"artist": tr.Artist,
		"path":   tr.Path,
	})
}

func (a *app) handleMedia(mk mkey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch mk {
	case mPlayPause:
		if a.ps.Playing() {
			a.ps.SetPaused(!a.ps.Paused())
			a.emitState()
		}
	case mVolUp:
		a.ps.SetVol(a.ps.Vol() + 5)
		a.emitState()
	case mVolDown:
		a.ps.SetVol(a.ps.Vol() - 5)
		a.emitState()
	case mMute:
		a.ps.SetVol(0)
		a.emitState()
	case mNext:
		a.nextManual()
	case mPrev:
		a.prevManual()
	}
}

func (a *app) nextManual() {
	if a.curID+1 < len(a.lib) {
		a.playByID(a.curID+1, 0)
	}
}
func (a *app) prevManual() {
	if a.curID-1 >= 0 {
		a.playByID(a.curID-1, 0)
	}
}

func (a *app) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ps.Playing() {
		a.eng.stop()
		time.Sleep(250 * time.Millisecond) // 等引擎收尾
	}
	a.restoreAudio()
}

// save 持久化当前模式/音量/设备/正在播的歌。
func (a *app) save() {
	cur := ""
	if a.curID >= 0 && a.curID < len(a.lib) {
		cur = a.lib[a.curID].Path
	}
	saveState(State{
		Mode:    a.mode,
		Device:  a.dev,
		Current: cur,
		Volume:  int(a.ps.Vol()),
		Dirs:    a.dirs,
	})
}

// ---------- 命令 ----------

// command 执行一行命令;返回 true 继续 REPL,false 退出。
func (a *app) command(line string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	f := strings.Fields(line)
	if len(f) == 0 {
		return true
	}
	cmd := strings.ToLower(f[0])
	switch cmd {
	case "q", "quit", "exit":
		return false
	case "help", "h", "?":
		a.emit(map[string]any{
			"evt": "help",
			"cmd": []string{
				"list               列出库",
				"play <id>          播放某首",
				"seek <sec>         跳到某秒",
				"pause|resume       暂停/继续",
				"next|prev          下一首/上一首",
				"vol <0..150>       音量",
				"mode <seq|single|rand|repeat>",
				"status             当前状态",
				"quit               退出",
			},
		})
	case "list", "ls":
		rows := make([]map[string]any, 0, len(a.lib))
		for i, tr := range a.lib {
			rows = append(rows, map[string]any{"id": i, "title": tr.Title, "artist": tr.Artist})
		}
		a.emit(map[string]any{"evt": "list", "n": len(a.lib), "tracks": rows})
	case "play", "p":
		if len(f) < 2 {
			a.emit(map[string]any{"evt": "error", "text": "用法: play <id>"})
			return true
		}
		id, err := strconv.Atoi(f[1])
		if err != nil {
			a.emitErr("id 不是数字")
			return true
		}
		a.playByID(id, 0)
	case "seek":
		if len(f) < 2 || a.curID < 0 {
			return true
		}
		sec, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return true
		}
		a.eng.playAt(a.lib[a.curID].Path, a.curID, sec)
	case "cover":
		if len(f) < 2 {
			a.emitErr("用法: cover <id>")
			return true
		}
		id, _ := strconv.Atoi(f[1])
		if id < 0 || id >= len(a.lib) {
			a.emitErr("没有该 id")
			return true
		}
		a.emit(map[string]any{"evt": "cover", "id": id, "path": readCover(a.lib[id].Path)})
	case "lyrics", "lyric":
		if len(f) < 2 {
			a.emitErr("用法: lyrics <id> [秒]")
			return true
		}
		id, _ := strconv.Atoi(f[1])
		if id < 0 || id >= len(a.lib) {
			a.emitErr("没有该 id")
			return true
		}
		tr := a.lib[id]
		lines := make([]map[string]any, 0, len(tr.lrc))
		for _, l := range tr.lrc {
			lines = append(lines, map[string]any{"t": l.T, "text": l.Text})
		}
		cur := map[string]any{}
		sec := -1.0
		if len(f) >= 3 {
			if v, err := strconv.ParseFloat(f[2], 64); err == nil {
				sec = v
			}
		}
		if sec < 0 && id == a.curID && a.ps.Rate() > 0 {
			sec = float64(a.ps.Pos()) / float64(a.ps.Rate())
		}
		if sec >= 0 {
			if i := curLRC(tr.lrc, sec); i >= 0 {
				cur = map[string]any{"t": tr.lrc[i].T, "text": tr.lrc[i].Text}
			}
		}
		a.emit(map[string]any{"evt": "lyrics", "id": id, "n": len(lines), "lines": lines, "current": cur, "at": sec})
	case "pause", "resume":
		if a.ps.Playing() {
			a.ps.SetPaused(!a.ps.Paused())
			a.emitState()
		}
	case "next", "n":
		a.nextManual()
	case "prev", "b", "previous":
		a.prevManual()
	case "vol", "volume":
		if len(f) < 2 {
			a.emitState()
			return true
		}
		v, err := strconv.Atoi(f[1])
		if err == nil {
			a.ps.SetVol(int32(v))
			a.save()
		}
		a.emitState()
	case "mode":
		if len(f) < 2 {
			a.emitState()
			return true
		}
		switch f[1] {
		case "seq", "顺序":
			a.mode = modeSeq
		case "single", "单曲":
			a.mode = modeSingle
		case "rand", "random", "随机":
			a.mode = modeRand
		case "repeat", "循环":
			a.mode = modeRepeat
		}
		a.save()
		a.emitState()
	case "status":
		a.emitState()
	case "scan", "rescan":
		a.rescan(nil)
		a.emit(map[string]any{"evt": "list", "n": len(a.lib), "note": "rescan done"})
	default:
		a.emitErr("未知命令:" + cmd + "(help 看帮助)")
	}
	return true
}

func (a *app) emitState() {
	cur := map[string]any{"id": -1}
	if a.curID >= 0 && a.curID < len(a.lib) {
		tr := a.lib[a.curID]
		cur = map[string]any{"id": a.curID, "title": tr.Title, "artist": tr.Artist}
	}
	a.emit(map[string]any{
		"evt":     "state",
		"playing": a.ps.Playing(),
		"paused":  a.ps.Paused(),
		"vol":     a.ps.Vol(),
		"pos":     a.ps.Pos(),
		"total":   a.ps.Total(),
		"rate":    a.ps.Rate(),
		"mode":    a.mode,
		"track":   cur,
	})
}

func (a *app) emitErr(s string) { a.emit(map[string]any{"evt": "error", "text": s}) }

// emit 输出一行 JSON(调用方须已持 a.mu,避免与 ticker 交错)。
func (a *app) emit(v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

// ticker 播放中每 500ms 发一次位置事件。
func (a *app) ticker() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		a.mu.Lock()
		if a.ps.Playing() {
			ev := map[string]any{"evt": "tick", "pos": a.ps.Pos(), "rate": a.ps.Rate(), "total": a.ps.Total()}
			if a.curID >= 0 && a.curID < len(a.lib) {
				tr := a.lib[a.curID]
				if len(tr.lrc) > 0 {
					sec := 0.0
					if r := a.ps.Rate(); r > 0 {
						sec = float64(a.ps.Pos()) / float64(r)
					}
					if i := curLRC(tr.lrc, sec); i >= 0 {
						ev["line"] = tr.lrc[i].Text
					}
				}
			}
			a.emit(ev)
		}
		a.mu.Unlock()
	}
}

// uniqStrs 保持顺序去重。
func uniqStrs(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range in {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
