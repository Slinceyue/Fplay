//go:build !linux && !windows

// 非 Linux 且非 Windows 平台:player 后端暂未实现。给空实现保证 go build 通过。
package player

import "io"

// ALSA 其它平台是占位类型:方法 no-op,OpenALSA 永远返回错误。
type ALSA struct{}

func (*ALSA) Close() error                            { return nil }
func (*ALSA) Pause(bool) error                        { return nil }
func (*ALSA) Write(p []byte) (int, error)             { return len(p), io.EOF }
func (*ALSA) AppendSample(buf []byte, v int32) []byte { return buf }
func (*ALSA) Exclusive() bool                         { return false }

// FindECHOADevice 其它平台是 no-op。
func FindECHOADevice() string { return "" }

// AlsaDevices 其它平台无 ALSA 设备列表,返回空。
func AlsaDevices() []AlsaDevice { return nil }

// OpenALSA 其它平台直接返回错误,主流程会提示。
func OpenALSA(name string, sampleRate, channels, bps int) (*ALSA, error) {
	return nil, errorString("ALSA 后端仅支持 Linux;其它平台后端待实现")
}

// DeviceMixFormat 其它平台无此概念,返回 ok=false。
func DeviceMixFormat(name string) (rate, bits, ch int, ok bool) { return 0, 0, 0, false }

type errorString string

func (e errorString) Error() string { return string(e) }
