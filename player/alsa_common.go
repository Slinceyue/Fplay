// 跨平台:输出设备/ALSA 句柄的公共类型。
package player

// AlsaDevice 是可供选择的 playback 输出设备。
type AlsaDevice struct {
	Name  string // Linux: "hw:<card>,<dev>";Windows: WASAPI 端点 ID;其它平台按各自实现。
	Label string // 可读描述
}

// appendPCM 把一个 int32 样本按设备容器编码追加进 buf(小端,内容左对齐)。
// bytesPer 是容器字节数(1/2/4),shift 是内容左移位数:24bit 塞进 4 字节容器时 shift=8。
// Linux(ALSA) 与 Windows(WASAPI) 复用同一编码,保证跨平台逐位一致。
func appendPCM(buf []byte, v int32, bytesPer int, shift uint) []byte {
	u := uint32(v) << shift
	for i := range bytesPer {
		buf = append(buf, byte(u>>uint(8*i)))
	}
	return buf
}
