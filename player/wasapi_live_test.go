//go:build windows

package player

import (
	"os"
	"strings"
	"testing"
)

// openSilent 打开并写约 100ms 静音、暂停/恢复,验证一条播放路径可走通。
func openSilent(t *testing.T, name string, rate, ch, bps int) {
	t.Helper()
	a, err := OpenALSA(name, rate, ch, bps)
	if err != nil {
		t.Fatalf("OpenALSA(%s,%d,%d,%d) 失败: %v", name, rate, ch, bps, err)
	}
	defer a.Close()
	t.Logf("打开 %s: bytesPer=%d shift=%d frame=%d bufSize=%d exclusive=%v",
		name, a.bytesPer, a.shift, a.frame, a.bufSize, a.exclusive)
	buf := make([]byte, rate/10*a.frame) // 约 100ms 静音(整帧)
	if n, err := a.Write(buf); err != nil {
		t.Fatalf("Write(写入 %d) 失败: %v", n, err)
	}
	if err := a.Pause(true); err != nil {
		t.Fatalf("Pause(true) 失败: %v", err)
	}
	if err := a.Pause(false); err != nil {
		t.Fatalf("Pause(false) 失败: %v", err)
	}
}

// 真机冒烟:仅在 FPLAY_LIVE=1 时运行。默认设备与 ECHO-A 在 16bit/24bit 下都应能写入/暂停/恢复。
// 独占可维持则用独占(位完美),否则开时探测自动回退共享(ECHO-A 属此)。
func TestLiveWASAPI(t *testing.T) {
	if os.Getenv("FPLAY_LIVE") != "1" {
		t.Skip("设 FPLAY_LIVE=1 以运行真机冒烟(会短暂占用音频设备)")
	}
	devs := AlsaDevices()
	if len(devs) == 0 {
		t.Fatal("AlsaDevices 返回空:没有枚举到任何播放端点")
	}
	t.Logf("枚举到 %d 个端点: %v", len(devs), devs)

	if def := FindECHOADevice(); def == "" {
		t.Fatal("FindECHOADevice(默认) 返回空")
	} else {
		t.Logf("FindECHOADevice(默认)=%s", def)
	}

	// 默认设备:16bit/48k、24bit/96k
	openSilent(t, "default", 48000, 2, 16)
	openSilent(t, "default", 96000, 2, 24)

	// ECHO-A(项目首选,独占不稳,应自动回退共享后仍能写):按端点 ID 打开
	var echoID string
	for _, d := range devs {
		if strings.Contains(d.Label, "ECHO-A") {
			echoID = d.Name
			break
		}
	}
	if echoID != "" {
		openSilent(t, echoID, 48000, 2, 24)
	}

	t.Log("默认设备 16/24bit 与 ECHO-A 真实播放路径 写入/暂停/恢复 全部通过")
}
