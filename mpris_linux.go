//go:build linux

// MPRIS 媒体服务:GNOME 顶栏原生显示常驻媒体卡片(封面/歌名/歌手/播放暂停/上下曲),
// 元数据/状态更新是静默的,不像 libnotify 那样每次弹窗。
package main

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"
)

const mprisPath = "/org/mpris/MediaPlayer2"

type mpris struct {
	bus *dbus.Conn
	ch  chan<- mkey

	mu             sync.Mutex
	cur            string
	title, artist  string
	art            string
	playing, pause bool
	lengthUs       int64
}

func newMpris(ch chan<- mkey) *mpris {
	bus, err := dbus.SessionBus()
	if err != nil {
		return nil
	}
	m := &mpris{bus: bus, ch: ch}
	bus.RequestName("org.mpris.MediaPlayer2.Fplay", dbus.NameFlagDoNotQueue)
	bus.Export(m, mprisPath, "org.mpris.MediaPlayer2")
	bus.Export(m, mprisPath, "org.mpris.MediaPlayer2.Player")
	bus.Export(m, mprisPath, "org.freedesktop.DBus.Properties")
	return m
}

// ---------- 播放控制(被 GNOME 按钮调用)----------

func (m *mpris) PlayPause() { m.send(mPlayPause) }
func (m *mpris) Play()      { m.send(mPlayPause) }
func (m *mpris) Pause()     { m.send(mPlayPause) }
func (m *mpris) Next()      { m.send(mNext) }
func (m *mpris) Previous()  { m.send(mPrev) }
func (m *mpris) Stop()      { m.send(mPlayPause) }
func (m *mpris) Raise()     {}
func (m *mpris) Quit()      {}

func (m *mpris) send(k mkey) {
	select {
	case m.ch <- k:
	default:
	}
}

// ---------- 状态更新(由主循环调用,静默)----------

// update 仅在播放状态/歌曲变化时刷新 MPRIS 属性,不产生弹窗。
// 锁粒度:把"读 FLAC 元数据"这种可能阻塞 IO 的动作放在锁外,
//
//	锁内只做"复制状态字段 + 构造 D-Bus 信号 + emit"。
func (m *mpris) update(path string, playing, paused bool) {
	if m == nil || m.bus == nil {
		return
	}
	// 决定要不要切歌:锁内读 cur,避免和并发 update 撞。
	m.mu.Lock()
	samePath := m.cur == path && path != ""
	m.mu.Unlock()

	var title, artist, art string
	if path != "" && !samePath {
		// 锁外读文件(可能阻塞几百毫秒~几秒)
		t, ar, ap := flacMeta(path)
		title, artist, art = t, ar, ap
		if title == "" {
			title = filepath.Base(path)
		}
	}

	// 临界区:写入状态 + 构造 D-Bus 信号 + emit 一次性原子完成。
	m.mu.Lock()
	defer m.mu.Unlock()

	if path != "" && path != m.cur {
		m.cur = path
		m.title = title
		m.artist = artist
		m.art = art
		m.lengthUs = 0
	}
	m.playing = playing
	m.pause = paused

	status := "Playing"
	if !playing {
		status = "Stopped"
	} else if paused {
		status = "Paused"
	}
	m.emitStatusLocked(status, path)
}

// emitStatusLocked 必须在持锁状态下调用;构造属性 map 并 emit 状态变更。
func (m *mpris) emitStatusLocked(status, path string) {
	meta := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/flacplayer/track")),
		"xesam:title":   dbus.MakeVariant(m.title),
	}
	if m.artist != "" {
		meta["xesam:artist"] = dbus.MakeVariant([]string{m.artist})
	}
	if m.lengthUs > 0 {
		meta["mpris:length"] = dbus.MakeVariant(m.lengthUs)
	}
	if m.art != "" {
		meta["mpris:artUrl"] = dbus.MakeVariant("file://" + m.art)
	} else if path != "" {
		meta["xesam:url"] = dbus.MakeVariant(path)
	}
	changed := map[string]dbus.Variant{
		"PlaybackStatus": dbus.MakeVariant(status),
		"Metadata":       dbus.MakeVariant(meta),
	}
	_ = m.bus.Emit(mprisPath, "org.freedesktop.DBus.Properties.PropertiesChanged",
		"org.mpris.MediaPlayer2.Player", changed, []string{})
}

// ---------- DBus 属性接口 ----------

func (m *mpris) Set(iface, prop string, value dbus.Variant) *dbus.Error { return nil }

func (m *mpris) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	all, _ := m.GetAll(iface)
	if v, ok := all[prop]; ok {
		return v, nil
	}
	return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs",
		[]interface{}{fmt.Sprintf("no such property %s", prop)})
}

func (m *mpris) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface == "org.mpris.MediaPlayer2.Player" {
		m.mu.Lock()
		status := "Playing"
		if !m.playing {
			status = "Stopped"
		} else if m.pause {
			status = "Paused"
		}
		title := m.title
		artist := m.artist
		art := m.art
		length := m.lengthUs
		m.mu.Unlock()

		meta := map[string]dbus.Variant{
			"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/flacplayer/track")),
			"xesam:title":   dbus.MakeVariant(title),
		}
		if artist != "" {
			meta["xesam:artist"] = dbus.MakeVariant([]string{artist})
		}
		if art != "" {
			meta["mpris:artUrl"] = dbus.MakeVariant("file://" + art)
		}
		if length > 0 {
			meta["mpris:length"] = dbus.MakeVariant(length)
		}
		return map[string]dbus.Variant{
			"PlaybackStatus": dbus.MakeVariant(status),
			"LoopStatus":     dbus.MakeVariant("None"),
			"Rate":           dbus.MakeVariant(1.0),
			"Shuffle":        dbus.MakeVariant(false),
			"Metadata":       dbus.MakeVariant(meta),
			"Volume":         dbus.MakeVariant(0.8),
			"Position":       dbus.MakeVariant(int64(0)),
			"MinimumRate":    dbus.MakeVariant(1.0),
			"MaximumRate":    dbus.MakeVariant(1.0),
			"CanGoNext":      dbus.MakeVariant(true),
			"CanGoPrevious":  dbus.MakeVariant(true),
			"CanPlay":        dbus.MakeVariant(true),
			"CanPause":       dbus.MakeVariant(true),
			"CanSeek":        dbus.MakeVariant(false),
			"CanControl":     dbus.MakeVariant(true),
		}, nil
	}
	if iface == "org.mpris.MediaPlayer2" {
		return map[string]dbus.Variant{
			"CanQuit":             dbus.MakeVariant(false),
			"CanRaise":            dbus.MakeVariant(false),
			"HasTrackList":        dbus.MakeVariant(false),
			"Identity":            dbus.MakeVariant("Fplay"),
			"DesktopEntry":        dbus.MakeVariant("Fplay"),
			"SupportedUriSchemes": dbus.MakeVariant([]string{"file"}),
			"SupportedMimeTypes":  dbus.MakeVariant([]string{"audio/flac"}),
		}, nil
	}
	return map[string]dbus.Variant{}, nil
}
