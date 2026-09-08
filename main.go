package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"FlacPlayer/player"
	"golang.org/x/term"
)

// 全局 UI 状态。
type app struct {
	dir     string
	entries []Entry // 当前目录条目(含 ..)
	cursor  int
	listH   int // 列表可显示行数(渲染时记录,供 PgUp/PgDn)

	files   []string // 当前目录音频文件(与展示同序)
	curIdx  int      // 正在播的下标(-1 无)
	current string   // 正在播的完整路径

	mode    string
	lyricOn bool
	listOn  bool // 是否显示文件列表(默认关:歌词为主)
	lyricSz int  // 歌词"大小":当前句上下各留几行(滚轮调 1..4)
	errMsg  string

	lyrics  []lrcLine
	lrcSong string // 已解析歌词对应的歌

	ps  *playState
	eng *engine

	audioStopped bool
	quit         bool

	dev     string
	devMenu bool
	devList []player.AlsaDevice
	devCur  int

	muted   bool  // 静音
	muteVol int32 // 静音前音量,恢复用

	posBase   int64     // 引擎位置基准(样本)
	posBaseAt time.Time // 基准对应的墙钟
	posMu     sync.Mutex

	triedFallback bool // 本曲是否已自动换过一次设备
}

func main() {
	if unlock := acquireLock(); unlock == nil {
		fmt.Fprintln(os.Stderr, "FlacPlayer 已在运行:请先退出旧实例(q)再启动。")
		os.Exit(1)
	} else {
		defer unlock()
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

// acquireLock 单实例锁:同一用户只允许一个播放器进程,避免同屏叠画。
func acquireLock() func() {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".config", "flacplayer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "player.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	st := loadState()

	a := &app{mode: "顺序", lyricOn: true, listOn: true, lyricSz: 1, curIdx: -1, ps: &playState{}}
	a.ps.SetVol(50) // 默认音量 50%
	if st.Volume > 0 {
		a.ps.SetVol(int32(st.Volume)) // 恢复上次音量
	}
	if st.Mode != "" {
		a.mode = st.Mode
	}
	// 输出设备:环境变量 FP_DEVICE > 上次选择 > ECHO-A > 列表第一个。
	a.dev = os.Getenv("FP_DEVICE")
	if a.dev == "" {
		a.dev = st.Device
	}
	if a.dev == "" {
		a.dev = player.FindECHOADevice()
	}
	if a.dev == "" {
		if ds := player.AlsaDevices(); len(ds) > 0 {
			a.dev = ds[0].Name
		}
	}
	if a.dev == "" {
		a.errMsg = "没找到任何 ALSA 输出设备"
	}

	dir := st.Folder
	if dir == "" {
		dir = filepath.Join(home, "音乐")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir = home
	}
	if err := a.cd(dir); err != nil {
		return err
	}

	rawRestore, err := enableRaw()
	if err != nil {
		return fmt.Errorf("无法进入原始终端: %w", err)
	}
	// 备用屏幕:启动即全清、退出还原,避免多次启动画面残留叠字。
	// 开鼠标上报(?1000):滚轮一格=一个事件,GNOME 就不会把一格拆成 5 个↑↓了。
	_, _ = os.Stdout.WriteString("\x1b[?1049h\x1b[H\x1b[2J\x1b[?25l\x1b[?1000h")
	restore := func() {
		_, _ = os.Stdout.WriteString("\x1b[?1000l\x1b[?25h\x1b[?1049l")
		rawRestore()
	}
	defer restore()

	a.eng = startEngine(a.dev, a.ps)

	// 监听耳机/键盘媒体键(暂停、音量、上下曲、静音)。
	media := make(chan mkey, 16)
	go mediaListener(media)

	// GNOME 媒体控制走 MPRIS:顶栏常驻卡片,切歌/暂停静默更新不弹窗。
	mp := newMpris(media)

	// 退出/信号时恢复系统声音与原始终端。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer a.shutdown()

	a.render()

	keys := make(chan key, 32)
	go keyLoop(keys)
	tick := time.NewTicker(100 * time.Millisecond) // 100ms 重画 + 平滑内插到 1/8 字符精度
	defer tick.Stop()

	notifSig := ""
	for !a.quit {
		select {
		case <-sig:
			a.quit = true
		case r := <-a.eng.out:
			a.handleRes(r)
		case k := <-keys:
			a.handleKey(k, keys)
		case mk := <-media:
			a.handleMedia(mk)
		case <-tick.C:
		}
		if a.quit {
			continue
		}
		// MPRIS 媒体卡静默同步:切歌/暂停/继续都反映在卡片上,不弹窗。
		sig := a.current
		switch {
		case !a.ps.Playing():
			sig += "|S"
		case a.ps.Paused():
			sig += "|P"
		default:
			sig += "|R"
		}
		if sig != notifSig {
			notifSig = sig
			if mp != nil {
				mp.update(a.current, a.ps.Playing(), a.ps.Paused())
			}
		}
		a.render()
	}
	return nil
}

func (a *app) shutdown() {
	if a.ps.Playing() {
		a.eng.stop()
		time.Sleep(250 * time.Millisecond) // 让引擎收尾关 ALSA
	}
	a.save()
	a.restoreAudio()
}

// loadLyrics 缓存当前歌曲的 .lrc(避免每帧读盘)。
func (a *app) loadLyrics() {
	if a.lrcSong == a.current {
		return
	}
	a.lrcSong = a.current
	a.lyrics = nil
	if a.current != "" {
		a.lyrics = parseLRCFile(a.current)
	}
}

func (a *app) save() {
	saveState(State{
		Folder:  a.dir,
		Mode:    a.mode,
		Device:  a.dev,
		Current: a.current,
		Volume:  int(a.ps.Vol()),
	})
}

// ---------- 目录/列表 ----------

func (a *app) cd(dir string) error {
	es, err := listDir(dir)
	if err != nil {
		a.errMsg = err.Error()
		return err
	}
	a.dir = dir
	a.entries = es
	a.cursor = 0
	a.files = filesIn(es)
	a.save()
	return nil
}

// ---------- 播放控制 ----------

func (a *app) audioEnsure() {
	if a.audioStopped {
		return
	}
	// ALSA 直写需独占设备,先停 PipeWire。
	exec.Command("systemctl", "--user", "stop",
		"pipewire.socket", "pipewire-pulse.socket",
		"pipewire.service", "wireplumber.service", "pipewire-pulse.service").Run()
	time.Sleep(300 * time.Millisecond)
	a.audioStopped = true
}

func (a *app) restoreAudio() {
	if !a.audioStopped {
		return
	}
	exec.Command("systemctl", "--user", "start",
		"pipewire.service", "wireplumber.service", "pipewire-pulse.service",
		"pipewire.socket", "pipewire-pulse.socket").Run()
	a.audioStopped = false
}

// markPosBase 记"当前引擎位置 = samplesNow,墙钟 = now"为进度条平滑的基准。
// 进度条渲染时:pos = samplesNow + (now - posBaseAt) * rate。
func (a *app) markPosBase(samplesNow int64) {
	a.posMu.Lock()
	a.posBase = samplesNow
	a.posBaseAt = time.Now()
	a.posMu.Unlock()
}

// doPlay 在 audio 列表里播 files[i](i<0 视为停止)。
func (a *app) doPlay(i int) {
	if i < 0 || i >= len(a.files) {
		a.curIdx = -1
		a.current = ""
		return
	}
	a.triedFallback = false
	a.audioEnsure()
	a.curIdx = i
	a.current = a.files[i]
	a.eng.play(a.current, i)
	a.markPosBase(0) // 新一首歌从 0 起算
	a.listOn = false // 播放即隐藏列表,歌词为主;按 t 随时调出列表
	a.save()         // 记住正在播的歌(供检测脚本/下次打开)
}

// playFrom 从某个文件、某秒开始在 a.dev 上播(选歌/自动换设备共用)。
func (a *app) playFrom(path string, idx int, sec float64) {
	a.audioEnsure()
	a.curIdx = idx
	a.current = path
	a.eng.playAt(path, idx, sec)
	a.markPosBase(int64(sec * float64(a.ps.Rate())))
	a.listOn = false
	a.save()
}

func (a *app) handleRes(r resMsg) {
	if r.reason == "err" {
		// 设备打不开时自动换一个可用设备继续(同进度),只自动试一次。
		if !a.triedFallback && a.current != "" &&
			(strings.Contains(r.errText, "输出设备") || strings.Contains(r.errText, "打开 ")) {
			if nd := a.nextDevice(); nd != "" && nd != a.dev {
				sec := 0.0
				if rt := a.ps.Rate(); rt > 0 {
					sec = float64(a.ps.Pos()) / float64(rt)
				}
				a.dev = nd
				a.eng.setDevice(nd)
				a.triedFallback = true
				a.errMsg = "设备打不开,已自动切到 " + nd + " 重试"
				a.playFrom(a.current, a.curIdx, sec)
				return
			}
		}
		a.errMsg = "播放出错: " + r.errText
		a.curIdx = -1
		a.current = ""
		return
	}
	if r.reason == "done" {
		a.nextAuto()
	}
}

// nextDevice 挑一个与当前不同的输出设备(优先 ECHO-A)。
func (a *app) nextDevice() string {
	if a.dev != "" {
		for _, d := range player.AlsaDevices() {
			if d.Name == a.dev {
				continue
			}
			if strings.Contains(strings.ToLower(d.Label), "echo") || strings.Contains(d.Label, "ECHO") {
				return d.Name
			}
		}
	}
	for _, d := range player.AlsaDevices() {
		if d.Name != a.dev {
			return d.Name
		}
	}
	return ""
}

// nextAuto 按模式决定一首自然播完后干什么。
func (a *app) nextAuto() {
	switch a.mode {
	case modeRepeat:
		a.doPlay(a.curIdx)
	case modeRand:
		if len(a.files) > 1 {
			j := rand.Intn(len(a.files))
			for j == a.curIdx {
				j = rand.Intn(len(a.files))
			}
			a.doPlay(j)
		} else {
			a.doPlay(a.curIdx)
		}
	case modeSingle:
		a.doPlay(-1)
	default: // 顺序
		if a.curIdx+1 < len(a.files) {
			a.doPlay(a.curIdx + 1)
		} else {
			a.doPlay(-1)
		}
	}
}

func (a *app) nextManual() {
	n := len(a.files)
	if n == 0 {
		return
	}
	switch a.mode {
	case modeRand:
		j := a.curIdx
		if n > 1 {
			for j == a.curIdx {
				j = rand.Intn(n)
			}
		}
		a.doPlay(j)
	default:
		if a.curIdx+1 < n {
			a.doPlay(a.curIdx + 1)
		} else {
			a.doPlay(0)
		}
	}
}

func (a *app) prevManual() {
	n := len(a.files)
	if n == 0 {
		return
	}
	// 已播 >3 秒则重播当前。
	if a.ps.Playing() && a.curIdx >= 0 {
		if sec := a.ps.Pos() / a.ps.Rate(); sec > 3 {
			a.doPlay(a.curIdx)
			return
		}
	}
	if a.curIdx-1 >= 0 {
		a.doPlay(a.curIdx - 1)
	} else {
		a.doPlay(n - 1)
	}
}

// ---------- 按键 ----------

func (a *app) handleKey(k key, keys <-chan key) {
	if a.devMenu {
		a.handleDevKey(k)
		return
	}
	switch k.kind {
	case keyUp:
		if a.cursor > 0 {
			a.cursor--
		}
	case keyDown:
		if a.cursor < len(a.entries)-1 {
			a.cursor++
		}
	case keyHome:
		a.cursor = 0
	case keyEnd:
		if n := len(a.entries); n > 0 {
			a.cursor = n - 1
		}
	case keyPgUp:
		a.pageMove(-1)
	case keyPgDn:
		a.pageMove(1)
	case keyLeft:
		a.seekRelative(-10)
	case keyRight:
		a.seekRelative(10)
	case keyEnter:
		a.activateCursor()
	case keyChar:
		a.handleChar(k.ch, keys)
	}
}

// pageMove 列表按整页翻。
func (a *app) pageMove(dir int) {
	n := len(a.entries)
	if n == 0 {
		return
	}
	page := a.listH
	if page < 2 {
		page = 10
	}
	c := a.cursor + dir*page
	if c < 0 {
		c = 0
	}
	if c >= n {
		c = n - 1
	}
	a.cursor = c
}

// seekRelative 相对当前进度快进/快退 sec 秒。
func (a *app) seekRelative(sec float64) {
	if !a.ps.Playing() || a.current == "" {
		return
	}
	rate := a.ps.Rate()
	total := a.ps.Total()
	cur := 0.0
	if rate > 0 {
		cur = float64(a.ps.Pos()) / float64(rate)
	}
	t := cur + sec
	if total > 0 && rate > 0 {
		dur := float64(total) / float64(rate)
		if t < 0 {
			t = 0
		}
		if t > dur {
			t = dur
		}
	} else if t < 0 {
		t = 0
	}
	a.eng.playAt(a.current, a.curIdx, t)
}

// changeVol 音量 ±delta(自动解除静音)。
func (a *app) changeVol(delta int32) {
	a.muted = false
	a.ps.SetVol(a.ps.Vol() + delta)
}

// toggleMute 静音/恢复。
func (a *app) toggleMute() {
	if a.muted {
		a.ps.SetVol(a.muteVol)
		a.muted = false
	} else {
		a.muteVol = a.ps.Vol()
		a.ps.SetVol(0)
		a.muted = true
	}
}

// handleMedia 耳机/键盘媒体键分发。
func (a *app) handleMedia(m mkey) {
	switch m {
	case mPlayPause:
		a.togglePlayPause()
	case mVolUp:
		a.changeVol(5)
	case mVolDown:
		a.changeVol(-5)
	case mMute:
		a.toggleMute()
	case mNext:
		a.nextManual()
	case mPrev:
		a.prevManual()
	}
}

// togglePause 暂停/继续(正在播才有效)。
func (a *app) togglePause() {
	if a.ps.Playing() {
		// 切换前先抓当前位置作为新基准(暂停/继续时渲染按基准+流逝推算)
		a.markPosBase(a.posBase + int64(time.Since(a.posBaseAt)*time.Duration(a.ps.Rate())/time.Second))
		a.ps.SetPaused(!a.ps.Paused())
	}
}

// togglePlayPause 媒体播放/暂停键:在播则暂停/继续;已停则从当前位置接着播。
func (a *app) togglePlayPause() {
	if a.ps.Playing() {
		a.ps.SetPaused(!a.ps.Paused())
		return
	}
	if a.current == "" || a.curIdx < 0 {
		return
	}
	sec := 0.0
	if r := a.ps.Rate(); r > 0 {
		sec = float64(a.ps.Pos()) / float64(r)
	}
	a.playFrom(a.current, a.curIdx, sec)
}

// handleDevKey 处理"输出设备选择"界面按键。
func (a *app) handleDevKey(k key) {
	switch k.kind {
	case keyUp:
		if a.devCur > 0 {
			a.devCur--
		}
	case keyDown:
		if a.devCur < len(a.devList)-1 {
			a.devCur++
		}
	case keyEnter:
		if a.devCur >= 0 && a.devCur < len(a.devList) {
			a.dev = a.devList[a.devCur].Name
			a.eng.setDevice(a.dev)
			a.errMsg = ""
			a.devMenu = false
			a.save()
			// 若正在播:立刻在当前位置换到新设备(同进度重播)。
			if a.ps.Playing() && a.current != "" {
				sec := 0.0
				if r := a.ps.Rate(); r > 0 {
					sec = float64(a.ps.Pos()) / float64(r)
				}
				a.eng.playAt(a.current, a.curIdx, sec)
			}
		}
	case keyEsc:
		a.devMenu = false
	}
}

func (a *app) handleChar(ch rune, keys <-chan key) {
	switch ch {
	case ' ':
		a.togglePause()
	case 'n', 'N':
		a.nextManual()
	case 'b', 'B':
		a.prevManual()
	case 'm', 'M':
		switch a.mode {
		case modeSeq:
			a.mode = modeRand
		case modeRand:
			a.mode = modeRepeat
		case modeRepeat:
			a.mode = modeSingle
		default:
			a.mode = modeSeq
		}
		a.save()
	case 'l', 'L':
		a.lyricOn = !a.lyricOn
	case 't', 'T':
		a.listOn = !a.listOn
	case ']':
		a.changeVol(5)
	case '[', '-':
		a.changeVol(-5)
	case 'v', 'V':
		a.toggleMute()
	case 'f', 'F':
		p := promptPath(keys, "目录路径: ")
		if p != "" {
			if err := a.cd(p); err != nil {
				a.errMsg = err.Error()
			}
		}
		a.render()
	case 'o', 'O':
		a.openDevMenu()
	case 'q', 'Q', 0x03:
		a.quit = true
	}
}

// openDevMenu 枚举 ALSA 输出设备并进入选择界面。
func (a *app) openDevMenu() {
	a.devList = player.AlsaDevices()
	a.devCur = 0
	for i, d := range a.devList {
		if d.Name == a.dev {
			a.devCur = i
			break
		}
	}
	a.devMenu = true
}

// activateCursor:目录进入/.. 上级;文件开始播放。
func (a *app) activateCursor() {
	if a.cursor < 0 || a.cursor >= len(a.entries) {
		return
	}
	e := a.entries[a.cursor]
	if e.Dir {
		if err := a.cd(e.Path); err != nil {
			a.errMsg = err.Error()
		}
		return
	}
	// 找到该文件在 files 里的下标。
	for i, p := range a.files {
		if p == e.Path {
			a.doPlay(i)
			return
		}
	}
}

// ---------- 渲染 ----------

// ANSI 配色。
const (
	cReset  = "\x1b[0m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[2m"
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[36m"
	cRev    = "\x1b[7m"
)

// lyricRows 歌词区固定显示行数。
const lyricRows = 5

// runeW 粗略判断字符显示宽度(东亚全角≈2)。
func runeW(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0x303E,
		r >= 0x3041 && r <= 0x33FF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xA000 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE4F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

func dispW(s string) int {
	n := 0
	for _, r := range s {
		n += runeW(r)
	}
	return n
}

// clipW 按显示宽度截断,超长末尾加省略号。
func clipW(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	total := 0
	for _, r := range runes {
		total += runeW(r)
	}
	if total <= width {
		return s
	}
	room := width - 1 // 留一格给 …
	w := 0
	keep := -1
	for i, r := range runes {
		rw := runeW(r)
		if w+rw > room {
			break
		}
		w += rw
		keep = i
	}
	if keep < 0 {
		return "…"
	}
	return string(runes[:keep+1]) + "…"
}

// padTo 右侧补空格到指定显示宽度。
func padTo(s string, width int) string {
	if gap := width - dispW(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// line 生成一行固定宽度文本并上色。
func line(s string, w int, color string) string {
	s = padTo(clipW(s, w), w)
	if color != "" {
		s = color + s + cReset
	}
	return s
}

func sep(w int) string { return line(strings.Repeat("─", w), w, cDim) }

func emit(sb *strings.Builder, s string) {
	sb.WriteString(s)
	sb.WriteString("\r\n")
}

func mmss(v float64) string {
	if v < 0 {
		v = 0
	}
	m := int(v) / 60
	s := int(v) % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}

func shortDev(dev string) string {
	if dispW(dev) <= 18 {
		return dev
	}
	return clipW(dev, 18)
}

func (a *app) render() {
	w, h := termSize()
	if w <= 0 {
		w, h = 80, 26
	}
	if a.devMenu {
		a.drawDeviceMenu(w, h)
		return
	}

	var sb strings.Builder
	sb.WriteString("\x1b[H")

	// 没在播且没歌时,自动把列表亮出来(不然没东西可选)。
	if a.current == "" && !a.ps.Playing() {
		a.listOn = true
	}

	// ---- 顶栏:状态 + 歌曲 ----
	now, mark := "待机", "■"
	if a.ps.Playing() {
		if a.ps.Paused() {
			now, mark = "暂停", "="
		} else {
			now, mark = "播放中", "▶"
		}
	}
	name := filepath.Base(a.current)
	if a.current == "" {
		name = "（未选歌曲）"
	}
	emit(&sb, line("  "+mark+"  "+now+"   "+name, w, cCyan+cBold))

	// ---- 顶栏:参数 ----
	volTxt := fmt.Sprintf("%d%%", a.ps.Vol())
	if a.muted {
		volTxt += "（静音）"
	}
	emit(&sb, line("  模式 "+a.mode+"    输出 "+shortDev(a.dev)+"    音量 "+volTxt, w, cDim))

	emit(&sb, sep(w))

	// ---- 进度条 ----
	emit(&sb, a.progressRow(w))

	emit(&sb, sep(w))

	// ---- 主体 ----
	lyrOn := a.lyricOn && a.current != ""
	if lyrOn {
		a.loadLyrics()
	}

	if !a.listOn {
		// 歌词为主的大屏:整块垂直铺满可用区,正在播的句子钉在正中。
		region := h - 5 - 2 // 顶栏2+分隔+进度+分隔=5,再留底部两行帮助
		if a.errMsg != "" {
			region--
		}
		if region < 4 {
			region = 4
		}
		if lyrOn && len(a.lyrics) > 0 {
			a.drawLyricCenter(&sb, w, region)
		} else {
			txt := "（这首没有同名 .lrc 歌词 · 按 t 显示文件列表选歌）"
			if !a.lyricOn {
				txt = "（歌词已关:按 l 打开 · 按 t 显示文件列表）"
			}
			emit(&sb, centerLine(txt, w, cDim))
		}
	} else {
		// 列表显示模式:小歌词在上,下面目录 + 文件列表。
		lyrH := 0
		if lyrOn {
			if len(a.lyrics) > 0 {
				lyrH = lyricRows
			} else {
				lyrH = 1
			}
			emit(&sb, line("  ♪ 歌词", w, cDim))
			for _, r := range a.lyricWindow(a.lyrics, w, lyrH) {
				emit(&sb, r)
			}
		}

		emit(&sb, line("  目录 "+clipW(a.dir, w-6)+"   （"+strconv.Itoa(len(a.files))+" 首）", w, cBold))

		listH := h - 5 // 顶栏2 + 分隔 + 进度 + 分隔
		listH -= lyrH
		if lyrOn {
			listH--
		}
		listH -= 3 // 目录头 + 两行帮助
		if a.errMsg != "" {
			listH--
		}
		if listH < 3 {
			listH = 3
		}
		a.drawList(&sb, w, listH)
	}

	// ---- 错误 ----
	if a.errMsg != "" {
		emit(&sb, line("  ⚠ "+a.errMsg, w, cRed))
	}

	// ---- 底部帮助(最后一行不加 \r\n,避免占满高度时把画面顶滚一行)----
	emit(&sb, line(" Enter 播放 · Space 暂停 · t 列表 · n/b 切歌 · ← → 快退/快进(±10s) · f 目录 · o 输出 · q 退出 · l 歌词", w, cDim))
	sb.WriteString(line(" 音量 ]加 [减(-也可)  v静音     字号:放大 Ctrl+Shift+=  缩小 Ctrl+-", w, cDim))

	sb.WriteString("\x1b[J")
	os.Stdout.WriteString(sb.String())
}

// progressRow 组合一条平滑进度条:填充精度 1/8,位置按"上次引擎上报 + 经过时间"实时算。
func (a *app) progressRow(w int) string {
	rate := a.ps.Rate()
	if rate <= 0 {
		return line("  --:-- / --:--", w, "")
	}
	// 实时位置:暂停时停在上次;播放时=引擎位置+(now-startedAt)*rate
	base := atomic.LoadInt64(&a.posBase) // 上次引擎上报时的 pos(样本)
	baseAt := a.posBaseAt
	if a.ps.Paused() || a.posBaseAt.IsZero() {
		baseAt = time.Time{} // 暂停:不再前进
	}
	curSamples := base
	if !baseAt.IsZero() {
		curSamples += int64(time.Since(baseAt) * time.Duration(rate) / time.Second)
	}
	if curSamples < 0 {
		curSamples = 0
	}
	cur := float64(curSamples) / float64(rate)
	dur := 0.0
	if t := a.ps.Total(); t > 0 {
		dur = float64(t) / float64(rate)
	}

	left := "  " + mmssTenth(cur) + " / " + mmssTenth(dur) + "  "
	bw := 30
	if bw > w-dispW(left)-6 {
		bw = w - dispW(left) - 6
	}
	if bw < 4 {
		bw = 4
	}
	pct := 0.0
	if dur > 0 {
		pct = cur / dur
	}
	if pct < 0 {
		pct = 0
	} else if pct > 1 {
		pct = 1
	}

	// 1/8 精度填充:总格数 = bw*8,按整 8 取整 + 余数用部分块字符
	const blocks = " ▏▎▍▌▋▊█" // 索引 0..7,7=满
	total := bw * 8
	done := int(pct * float64(total))
	full := done / 8
	part := done % 8
	bar := strings.Repeat("█", full)
	if part > 0 {
		bar += string(blocks[part])
	}
	if tail := bw - full - 1; tail > 0 {
		bar += strings.Repeat("░", tail)
	} else if tail == 0 {
		// 没余数且刚好满
	}
	barTxt := left + "[" + bar + "]"
	return line(barTxt+fmt.Sprintf("  %3.0f%%", pct*100), w, "")
}

// mmssTenth 01:23.4 形式,十秒一秒内显示 0:00.0。
func mmssTenth(v float64) string {
	if v < 0 {
		v = 0
	}
	m := int(v) / 60
	s := int(v) % 60
	d := int((v - float64(int(v))) * 10)
	return fmt.Sprintf("%d:%02d.%d", m, s, d)
}

// drawList 文件列表,窗口随 cursor 滚动。
func (a *app) drawList(sb *strings.Builder, w, listH int) {
	a.listH = listH // 供 PgUp/PgDn 整页翻用
	if len(a.entries) == 0 {
		emit(sb, line("  （空目录）", w, cDim))
		return
	}
	top := a.cursor - listH/2
	if top < 0 {
		top = 0
	}
	if top+listH > len(a.entries) {
		top = len(a.entries) - listH
		if top < 0 {
			top = 0
		}
	}
	for i := top; i < len(a.entries) && i < top+listH; i++ {
		e := a.entries[i]
		pre := "  "
		color := ""
		kindMark := "♪"
		if e.Dir {
			kindMark = "▸"
		}
		playing := !e.Dir && e.Path == a.current && a.ps.Playing()
		if playing {
			kindMark = "▶"
		}
		if i == a.cursor {
			pre = "> "
			if e.Dir {
				color = cYellow + cBold + cRev
			} else {
				color = cGreen + cBold + cRev
			}
		} else if e.Dir {
			color = cDim
		} else if playing {
			color = cCyan
		}
		emit(sb, line(pre+" "+kindMark+" "+e.Name, w, color))
	}
}

// lyricWindow 以当前行为中心,产出恰好 rows 行(不足补空),整行铺满屏宽。
func (a *app) lyricWindow(ls []lrcLine, w, rows int) []string {
	out := make([]string, rows)
	if len(ls) == 0 {
		out[0] = line("  （无同名 .lrc 歌词）", w, cDim)
		for i := 1; i < rows; i++ {
			out[i] = line("", w, "")
		}
		return out
	}
	t := float64(a.ps.Pos()) / float64(a.ps.Rate())
	cur := curLRC(ls, t)
	top := cur - rows/2
	if top < 0 {
		top = 0
	}
	if top+rows > len(ls) {
		top = len(ls) - rows
		if top < 0 {
			top = 0
		}
	}
	idx := 0
	pm := "▶"
	if a.ps.Paused() {
		pm = "="
	}
	for i := top; i < top+rows && i < len(ls); i++ {
		if i == cur {
			out[idx] = line("   "+pm+"  "+ls[i].Text, w, cYellow+cBold)
		} else {
			out[idx] = line("       "+ls[i].Text, w, cDim)
		}
		idx++
	}
	for ; idx < rows; idx++ {
		out[idx] = line("", w, "")
	}
	return out
}

func (a *app) setLyricSz(v int) {
	if v < 1 {
		v = 1
	}
	if v > 4 {
		v = 4
	}
	a.lyricSz = v
}

// centerLine 文本按屏幕宽度水平居中。
func centerLine(s string, w int, color string) string {
	if dispW(s) >= w {
		return line(s, w, color)
	}
	lp := (w - dispW(s)) / 2
	return line(strings.Repeat(" ", lp)+s, w, color)
}

// drawLyricCenter 大屏:正在播的句子固定在垂直正中,前后句按 lyricSz 行距排开,整屏水平居中。
func (a *app) drawLyricCenter(sb *strings.Builder, w, rows int) {
	ls := a.lyrics
	if len(ls) == 0 {
		return
	}
	step := a.lyricSz
	if step < 1 {
		step = 1
	}
	t := float64(a.ps.Pos()) / float64(a.ps.Rate())
	cur := curLRC(ls, t)
	if cur < 0 || cur >= len(ls) {
		cur = 0
	}
	mid := rows / 2
	slot := make([]*lrcLine, rows)
	slot[mid] = &ls[cur]
	for k := 1; ; k++ {
		r, i := mid-k*step, cur-k
		if r < 0 || i < 0 {
			break
		}
		slot[r] = &ls[i]
	}
	for k := 1; ; k++ {
		r, i := mid+k*step, cur+k
		if r >= rows || i >= len(ls) {
			break
		}
		slot[r] = &ls[i]
	}
	for r := 0; r < rows; r++ {
		if slot[r] == nil {
			emit(sb, line("", w, ""))
			continue
		}
		if r == mid {
			emit(sb, centerLine(slot[r].Text, w, cYellow+cBold))
		} else {
			emit(sb, centerLine(slot[r].Text, w, cDim))
		}
	}
}

// drawDeviceMenu 渲染"输出设备选择"界面。
func (a *app) drawDeviceMenu(w, h int) {
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	emit(&sb, line("  输出设备选择   当前: "+shortDev(a.dev), w, cCyan+cBold))
	emit(&sb, sep(w))
	rows := h - 4
	if rows < 3 {
		rows = 3
	}
	top := a.devCur - rows/2
	if top < 0 {
		top = 0
	}
	if top+rows > len(a.devList) {
		top = len(a.devList) - rows
		if top < 0 {
			top = 0
		}
	}
	for i := top; i < len(a.devList) && i < top+rows; i++ {
		d := a.devList[i]
		pre, mark, color := "  ", "  ", cDim
		if d.Name == a.dev {
			mark = "●"
		}
		if i == a.devCur {
			pre = "> "
			color = cGreen + cBold + cRev
		}
		emit(&sb, line(pre+mark+"  "+d.Label+"  ["+d.Name+"]", w, color))
	}
	if a.errMsg != "" {
		emit(&sb, line("  ⚠ "+a.errMsg, w, cRed))
	}
	sb.WriteString(line(" ↑/↓ 选择    Enter 确认    Esc 返回", w, cDim))
	sb.WriteString("\x1b[J")
	os.Stdout.WriteString(sb.String())
}

func termSize() (int, int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0, 0
	}
	return w, h
}
