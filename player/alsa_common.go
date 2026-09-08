// 跨平台:输出设备/ALSA 句柄的公共类型。
package player

// AlsaDevice 是可供选择的 playback 输出设备。
type AlsaDevice struct {
	Name  string // "hw:<card>,<dev>"
	Label string // 可读描述
}
