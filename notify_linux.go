//go:build linux

// 常驻通知栏播放卡片:封面/歌名/歌手/暂停/上一首/下一首。
// 通过 D-Bus 的 org.freedesktop.Notifications 实现;无桌面通知服务时自动跳过。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

type notifier struct {
	bus        *dbus.Conn
	obj        dbus.BusObject
	id         uint32
	cover      string    // 上次写的封面临时文件
	lastToggle time.Time // 防 GNOME 一次点击发两条 ActionInvoked
}

func newNotifier(ch chan<- mkey) *notifier {
	n := &notifier{}
	bus, err := dbus.SessionBus()
	if err != nil {
		return n // 无桌面会话,静默跳过
	}
	n.bus = bus
	n.obj = bus.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")

	// 监听按钮被点:ActionInvoked(id, key)。
	bus.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.Notifications"),
		dbus.WithMatchMember("ActionInvoked"),
	)
	sig := make(chan *dbus.Signal, 8)
	bus.Signal(sig)
	go func() {
		for s := range sig {
			if len(s.Body) < 2 {
				continue
			}
			id, ok1 := s.Body[0].(uint32)
			key, ok2 := s.Body[1].(string)
			if !ok1 || !ok2 || id != n.id {
				continue
			}
			switch key {
			case "default", "toggle":
				now := time.Now()
				if now.Sub(n.lastToggle) < 350*time.Millisecond {
					continue // 同一次点击会收到两条,只认一次
				}
				n.lastToggle = now
				ch <- mPlayPause
			case "prev":
				ch <- mPrev
			case "next":
				ch <- mNext
			}
		}
	}()
	return n
}

// update 刷新通知卡片。path 为空则收起通知。
func (n *notifier) update(path string, playing, paused bool) {
	if n.bus == nil || n.obj == nil {
		return
	}
	if path == "" {
		n.close()
		return
	}

	title, artist, coverPath := flacMeta(path)
	if coverPath != n.cover {
		n.cover = coverPath // 供 image-path
	}
	state := "播放中"
	if paused {
		state = "暂停"
	}
	summary := title
	if summary == "" {
		summary = filepath.Base(path)
	}
	body := state
	if artist != "" {
		body = artist + " · " + state
	}

	hints := map[string]dbus.Variant{}
	if n.cover != "" {
		hints["image-path"] = dbus.MakeVariant(n.cover)
	} else {
		hints["image-path"] = dbus.MakeVariant("/usr/share/icons/Adwaita/256x256/legacy/audio-x-generic.png")
	}
	// 让 GNOME 认出应用并渲染操作按钮。
	hints["desktop-entry"] = dbus.MakeVariant("flacplayer")
	// 第一对 default = 点卡片主体触发播放/暂停(否则会被 GNOME 当成"关闭")。
	actions := []string{"default", "播放/暂停", "prev", "上一首", "next", "下一首"}

	// 超时传 0 = 永不自动消失(常驻);-1 是交给服务器默认,GNOME 一会儿就收掉。
	call := n.obj.Call("org.freedesktop.Notifications.Notify", 0,
		"FlacPlayer", n.id, "", summary, body, actions, hints, int32(0))
	// 用回包里的 id 更新替换目标,否则每次都会另起一张新卡。
	if call.Err == nil && len(call.Body) > 0 {
		if id, ok := call.Body[0].(uint32); ok {
			n.id = id
		}
	}
}

func (n *notifier) close() {
	if n.bus == nil || n.obj == nil {
		return
	}
	if n.id != 0 {
		n.obj.Call("org.freedesktop.Notifications.CloseNotification", 0, n.id)
		n.id = 0
	}
}

// flacMeta 读 FLAC 元数据里的标题/艺术家/内嵌封面,封面写到临时文件返回路径。
func flacMeta(path string) (title, artist, cover string) {
	st, err := flac.ParseFile(path)
	if err != nil {
		return "", "", ""
	}
	for _, b := range st.Blocks {
		switch body := b.Body.(type) {
		case *meta.VorbisComment:
			for _, t := range body.Tags {
				k := strings.ToUpper(strings.TrimSpace(t[0]))
				v := strings.TrimSpace(t[1])
				switch k {
				case "TITLE":
					if title == "" {
						title = v
					}
				case "ARTIST":
					if artist == "" {
						artist = v
					}
				}
			}
		case *meta.Picture:
			if cover == "" && len(body.Data) > 0 {
				ext := ".jpg"
				mime := strings.ToLower(body.MIME)
				if strings.Contains(mime, "png") {
					ext = ".png"
				}
				f, err := os.CreateTemp("", "flac-cover-*"+ext)
				if err == nil {
					_, _ = f.Write(body.Data)
					_ = f.Close()
					cover = f.Name()
				}
			}
		}
	}
	return title, artist, cover
}
