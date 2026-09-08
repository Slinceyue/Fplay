package decode

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// SeekTableCache 把已建好的 SeekTable 序列化到本地磁盘,跨进程/跨播放复用。
// 同一个文件再 Seek 时不再扫整首,直接拿表挂回去。
type SeekTableCache struct {
	mu sync.Mutex
}

var seekCache SeekTableCache

func seekTableDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	d := filepath.Join(home, ".config", "flacplayer", "seektables")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// key 取 path + MD5 + 修改时间,改文件失效。
func seekKey(path string, info *meta.StreamInfo) string {
	if path == "" || info == nil {
		return ""
	}
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sum := md5.Sum([]byte(path))
	raw := []byte(hex.EncodeToString(sum[:]))
	raw = append(raw, 0)
	raw = append(raw, info.MD5sum[:]...)
	raw = append(raw, 0)
	raw = append(raw, st.ModTime().String()...)
	return filepath.Join(seekTableDir(), hex.EncodeToString(raw)+".bin")
}

// Save 表落盘;失败忽略。
func (c *SeekTableCache) Save(path string, st *meta.SeekTable, info *meta.StreamInfo) {
	if st == nil || info == nil {
		return
	}
	key := seekKey(path, info)
	if key == "" {
		return
	}
	data := encodeSeekTable(st)
	if data == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = os.WriteFile(key, data, 0o644)
}

// Load 命中且路径/MD5/时间匹配就返回;否则 nil。
func (c *SeekTableCache) Load(path string, info *meta.StreamInfo) *meta.SeekTable {
	if info == nil {
		return nil
	}
	key := seekKey(path, info)
	if key == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(key)
	if err != nil {
		return nil
	}
	return decodeSeekTable(data)
}

// ApplyToStream 命中时把表挂到 s 的私有 seekTable 字段(通过反射 + unsafe 写入)。
func (c *SeekTableCache) ApplyToStream(s *flac.Stream, path string) bool {
	if s == nil || s.Info == nil {
		return false
	}
	st := c.Load(path, s.Info)
	if st == nil {
		return false
	}
	return setSeekTable(s, st)
}

// setSeekTable 通过反射 + unsafe 写入 s.seekTable。
func setSeekTable(s *flac.Stream, st *meta.SeekTable) bool {
	v := reflect.ValueOf(s).Elem().FieldByName("seekTable")
	if !v.IsValid() || !v.CanSet() {
		return false
	}
	v.Set(reflect.ValueOf(st))
	return true
}

// 私有手序列化:每个 SeekPoint 18 字节(uint64+uint64+uint16),前缀 4 字节点数。
func encodeSeekTable(st *meta.SeekTable) []byte {
	if st == nil {
		return nil
	}
	out := make([]byte, 0, 4+18*len(st.Points))
	out = appendUint32(out, uint32(len(st.Points)))
	for _, p := range st.Points {
		out = appendUint64(out, p.SampleNum)
		out = appendUint64(out, p.Offset)
		out = appendUint16(out, p.NSamples)
	}
	return out
}
func decodeSeekTable(b []byte) *meta.SeekTable {
	if len(b) < 4 {
		return nil
	}
	n, off := readUint32(b, 0)
	if len(b) < 4+18*int(n) {
		return nil
	}
	st := &meta.SeekTable{Points: make([]meta.SeekPoint, n)}
	for i := uint32(0); i < n; i++ {
		sn, _ := readUint64(b, off)
		off += 8
		ofs, _ := readUint64(b, off)
		off += 8
		ns, _ := readUint16(b, off)
		off += 2
		st.Points[i] = meta.SeekPoint{SampleNum: sn, Offset: ofs, NSamples: ns}
	}
	return st
}

func appendUint16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
func appendUint64(b []byte, v uint64) []byte {
	return append(b, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
func readUint16(b []byte, i int) (uint16, int) { return uint16(b[i])<<8 | uint16(b[i+1]), i + 2 }
func readUint32(b []byte, i int) (uint32, int) {
	return uint32(b[i])<<24 | uint32(b[i+1])<<16 | uint32(b[i+2])<<8 | uint32(b[i+3]), i + 4
}
func readUint64(b []byte, i int) (uint64, int) {
	return uint64(b[i])<<56 | uint64(b[i+1])<<48 | uint64(b[i+2])<<40 | uint64(b[i+3])<<32 |
		uint64(b[i+4])<<24 | uint64(b[i+5])<<16 | uint64(b[i+6])<<8 | uint64(b[i+7]), i + 8
}

// GC 进程启动时清理 7 天没动过的表。
func GC() {
	d := seekTableDir()
	if d == "" {
		return
	}
	des, _ := os.ReadDir(d)
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		p := filepath.Join(d, de.Name())
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if time.Since(fi.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(p)
		}
	}
}

func init() { GC() }
