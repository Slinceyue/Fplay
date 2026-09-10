//go:build windows

// WASAPI 音频后端:用 x/sys/windows 加载 DLL + 手写 COM vtable(经 stdlib syscall.SyscallN)
// 调用,0 CGO。优先事件驱动的独占模式(逐位直出、精确采样率,不重采样),独占失败自动
// 回退共享模式(AUTOCONVERTPCM,交给系统换算,兜底才允许重采样)。
//
// COM 句柄统一用 unsafe.Pointer 承载,仅在交给 SyscallN 的边界转成 uintptr——
// 这样 go vet 的 unsafeptr 检查不会误报(它只拦 uintptr→unsafe.Pointer 的反向转换)。
package player

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var debugWASAPI = os.Getenv("FPLAY_WAPI_DEBUG")

// ---------- 常量 ----------

const (
	wfmtTagExtensible = 0xFFFE // WAVEFORMATEX.wFormatTag 的 EXTENSIBLE 标记

	shareModeShared                 = 0x0 // AUDCLNT_SHAREMODE_SHARED
	shareModeExclusive              = 0x1 // AUDCLNT_SHAREMODE_EXCLUSIVE
	streamFlagsEventCallback        = 0x00040000
	streamFlagsAutoConvertPCM       = 0x80000000
	deviceStateActive               = 0x1    // DEVICE_STATE_ACTIVE
	eRender                         = 0x0    // EDataFlow
	eConsole                        = 0x0    // ERole
	clsctxAll                       = 0x17   // CLSCTX_ALL
	vtLPWSTR                        = 0x1F   // PROPVARIANT 里的 VT_LPWSTR
	coinitMTA                       = 0x0    // COINIT_MULTITHREADED
	defaultPeriodHns          int64 = 100000 // 10ms(100ns 单位),设备无周期时兜底

	// AUDCLNT_E_BUFFER_SIZE_NOT_ALIGNED:独占缓冲与设备周期未对齐(需按实际缓冲帧数重算)。
	audclntBufferSizeNotAligned = 0x88890019
)

// ---------- 需要的 GUID ----------

var (
	clsidMMDeviceEnumerator = mustGUID("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iidIMMDeviceEnumerator  = mustGUID("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	iidIMMDevice            = mustGUID("{D666063F-1587-4E43-81F1-B948E807363F}")
	iidIAudioClient         = mustGUID("{1CB9AD4C-DBFA-4C32-B178-C2F568A703B2}")
	iidIAudioRenderClient   = mustGUID("{F294ACFC-3146-4483-A7BF-ADDCA7C260E2}")
	pcmSubFormat            = mustGUID("{00000001-0000-0010-8000-00AA00389B71}")
	pkeyFriendlyName        = propertyKey{mustGUID("{A45C254E-DF1C-4EFD-8020-67D146A850E0}"), 14}
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic("player: 非法 GUID 常量: " + s)
	}
	return g
}

// ---------- 基础 COM 工具 ----------

// comSlot 取 COM 接口 IUnknown 之后第 slot 个方法(用 vtable 槽偏移),返回函数指针。
func comSlot(iface unsafe.Pointer, slot int) uintptr {
	vptr := *(*unsafe.Pointer)(iface) // vtable 指针
	return *(*uintptr)(unsafe.Add(vptr, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
}

// comCall 调用接口第 slot 个方法(自动带上 this 指针),返回 r1(HRESULT 或结果值)。
func comCall(iface unsafe.Pointer, slot int, args ...uintptr) uintptr {
	r1, _, _ := syscall.SyscallN(comSlot(iface, slot), append([]uintptr{uintptr(iface)}, args...)...)
	return r1
}

// comRelease 调用 IUnknown::Release(槽 2)。
func comRelease(iface unsafe.Pointer) {
	if iface != nil {
		comCall(iface, 2)
	}
}

// hrOK HRESULT 是否成功(S_FALSE 及以上):符号位为 0。
func hrOK(hr uintptr) bool { return int32(hr) >= 0 }

// ap 把任意指针转成 uintptr,给 SyscallN 传址参数用(指针→uintptr,vet 不误报)。
func ap[T any](v *T) uintptr { return uintptr(unsafe.Pointer(v)) }

// windows 进程内做一次 MTA COM 初始化;不再 CoUninitialize(单次 CLI,MTA 对象可跨线程用)。
var comInitOnce sync.Once

func comInit() { comInitOnce.Do(func() { _ = windows.CoInitializeEx(0, coinitMTA) }) }

// utf16PtrToString 读 NUL 结尾的宽字符串指针转 Go string(空指针返回 "")。
func utf16PtrToString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	n := 0
	for n < 4096 { // 名称不会这么长,防越界
		if *(*uint16)(unsafe.Add(p, uintptr(n)*2)) == 0 {
			break
		}
		n++
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(p), n))
}

// ---------- DLL 导出函数(LazyDLL) ----------

var (
	pcCoCreateInstance    = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
	pcCreateEventW        = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateEventW")
	pcWaitForSingleObject = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitForSingleObject")
	pcCloseHandle         = windows.NewLazySystemDLL("kernel32.dll").NewProc("CloseHandle")
	// MMCSS:把线程加入 "Pro Audio" 类别(降低音频爆音)。
	pcAvSetMmThreadCharacteristicsW = windows.NewLazySystemDLL("avrt.dll").NewProc("AvSetMmThreadCharacteristicsW")
)

func coCreateInstance(clsid, iid *windows.GUID, out *unsafe.Pointer) error {
	r1, _, _ := pcCoCreateInstance.Call(ap(clsid), 0, clsctxAll, ap(iid), ap(out))
	if !hrOK(r1) || *out == nil {
		return fmt.Errorf("player: CoCreateInstance 失败: 0x%08x", r1)
	}
	return nil
}

// ---------- 结构(手写内存布局) ----------

// propertyKey 是 IPropertyStore::GetValue 的 key(PROPERTYKEY)。
type propertyKey struct {
	fmtID windows.GUID
	pid   uint32
}

// propVariant 是 PROPVARIANT 的 64 位内存布局(共 16 字节)。
// vt 之后是 3 个保留 WORD,再是 8 字节的 union(字符串指针并存于其中)。
type propVariant struct {
	vt         uint16
	r1, r2, r3 uint16
	data       uint64
}

func (pv *propVariant) lpwstr(free bool) string {
	if pv.vt != vtLPWSTR {
		return ""
	}
	p := *(*unsafe.Pointer)(unsafe.Pointer(&pv.data))
	if p == nil {
		return ""
	}
	s := utf16PtrToString(p)
	if free {
		windows.CoTaskMemFree(p)
	}
	return s
}

// WAVEFORMATEX 头(PCM 基础部分,18 字节)。用于读设备 GetMixFormat 返给的头。
type waveFormatEx struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

// WAVEFORMATEXTENSIBLE(40 字节)。必须以扁平字段声明:若嵌入 waveFormatEx,其对齐(4)
// 会把外层整体对齐,导致 valid/mask/subFormat 后移到 20/24/28,与 C 布局(18/20/24)
// 不符,WASAPI 会以 E_INVALIDARG 拒绝。扁平后 Go 自然排布为精确的 C 布局。
type waveFormatExtensible struct {
	wFormatTag       uint16
	nChannels        uint16
	nSamplesPerSec   uint32
	nAvgBytesPerSec  uint32
	nBlockAlign      uint16
	wBitsPerSample   uint16
	cbSize           uint16
	samplesValidBits uint16 // Samples.wValidBitsPerSample
	dwChannelMask    uint32
	subFormat        windows.GUID
}

// formatCandidate 一种可尝试的样本容器(决定 AppendSample 的 bytesPer/shift)。
type formatCandidate struct {
	wfx      *waveFormatExtensible
	bytesPer int
	shift    uint
}

// newCandidate 按「容器位 / 有效位 / 容器字节」构造一个候选。
func newCandidate(rate, channels, containerBits, validBits, bytesPer int) formatCandidate {
	blockAlign := channels * bytesPer
	return formatCandidate{
		wfx: &waveFormatExtensible{
			wFormatTag:       wfmtTagExtensible,
			nChannels:        uint16(channels),
			nSamplesPerSec:   uint32(rate),
			nAvgBytesPerSec:  uint32(rate * blockAlign),
			nBlockAlign:      uint16(blockAlign),
			wBitsPerSample:   uint16(containerBits),
			cbSize:           22,
			samplesValidBits: uint16(validBits),
			dwChannelMask:    chanMask(channels),
			subFormat:        pcmSubFormat,
		},
		bytesPer: bytesPer,
		shift:    uint(containerBits - validBits),
	}
}

// buildCandidates 按源位深给出可尝试的容器候选,按优先级排列。
// 独占模式会用 IsFormatSupported 挑设备原生支持的那个(见 selectExclusiveFormat),
// 以便「任何位深/采样率都尽量原参数直出」;共享模式取第一个交给系统换算。
// 24bit:优先 32bit 容器(Sample<<8 左对齐,兼容性最好),再试真正的 24bit 容器(S24_3)。
func buildCandidates(rate, channels, bps int) ([]formatCandidate, error) {
	switch bps {
	case 8:
		return []formatCandidate{newCandidate(rate, channels, 8, 8, 1)}, nil
	case 16:
		return []formatCandidate{newCandidate(rate, channels, 16, 16, 2)}, nil
	case 24:
		return []formatCandidate{
			newCandidate(rate, channels, 32, 24, 4),
			newCandidate(rate, channels, 24, 24, 3),
		}, nil
	case 32:
		return []formatCandidate{newCandidate(rate, channels, 32, 32, 4)}, nil
	default:
		return nil, fmt.Errorf("player: 不支持的位深 %d bit(仅 8/16/24/32)", bps)
	}
}

// selectExclusiveFormat 用 IsFormatSupported(EXCLUSIVE) 在候选里挑设备原生支持的格式。
// 该调用不消耗 client,可多次调用;全不支持返回 nil。
func selectExclusiveFormat(client unsafe.Pointer, cands []formatCandidate) *formatCandidate {
	for i := range cands {
		hr := comCall(client, 7, uintptr(shareModeExclusive), ap(cands[i].wfx), 0) // IsFormatSupported
		if hrOK(hr) {
			return &cands[i]
		}
	}
	return nil
}

// chanMask 给出常见声道数的声道遮罩;拿不准时给 2(FL|FR)。
func chanMask(ch int) uint32 {
	switch ch {
	case 1:
		return 0x4 // FRONT_CENTER
	case 2:
		return 0x3 // FL|FR
	case 3:
		return 0x7
	case 4:
		return 0x33
	case 5:
		return 0x37
	case 6:
		return 0x3F // 5.1
	default:
		return 0x3
	}
}

// ---------- 设备枚举 ----------

// deviceID 取 IMMDevice 的端点 ID(本函数内部负责 CoTaskMemFree)。
func deviceID(dev unsafe.Pointer) string {
	var p unsafe.Pointer
	hr := comCall(dev, 5, ap(&p)) // IMMDevice::GetId
	if !hrOK(hr) || p == nil {
		return ""
	}
	defer windows.CoTaskMemFree(p)
	return utf16PtrToString(p)
}

// deviceFriendly 读友好名(PKEY_Device_FriendlyName)。
func deviceFriendly(dev unsafe.Pointer) string {
	var store unsafe.Pointer
	if !hrOK(comCall(dev, 4, 0x0, ap(&store))) || store == nil { // OpenPropertyStore(STGM_READ)
		return "未命名设备"
	}
	defer comRelease(store)
	var pv propVariant
	comCall(store, 5, ap(&pkeyFriendlyName), ap(&pv)) // IPropertyStore::GetValue
	if s := pv.lpwstr(true); s != "" {
		return s
	}
	return "未命名设备"
}

// defaultDeviceID 取默认播放入口(用于给默认设备标「(默认)」)。
func defaultDeviceID(enc unsafe.Pointer) string {
	var dev unsafe.Pointer
	if !hrOK(comCall(enc, 4, eRender, eConsole, ap(&dev))) || dev == nil { // GetDefaultAudioEndpoint
		return ""
	}
	defer comRelease(dev)
	return deviceID(dev)
}

// AlsaDevices 枚举系统里所有激活的 WASAPI 播放端点。
func AlsaDevices() []AlsaDevice {
	comInit()
	var enc unsafe.Pointer
	if err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator, &enc); err != nil {
		return nil
	}
	defer comRelease(enc)

	var coll unsafe.Pointer
	if !hrOK(comCall(enc, 3, eRender, deviceStateActive, ap(&coll))) || coll == nil { // EnumAudioEndpoints
		return nil
	}
	defer comRelease(coll)

	var count uint32
	comCall(coll, 3, ap(&count)) // IMMDeviceCollection::GetCount

	def := defaultDeviceID(enc)
	out := make([]AlsaDevice, 0, count)
	for i := uint32(0); i < count; i++ {
		var dev unsafe.Pointer
		if !hrOK(comCall(coll, 4, uintptr(i), ap(&dev))) || dev == nil { // Item
			continue
		}
		id := deviceID(dev)
		label := deviceFriendly(dev)
		comRelease(dev)
		if id == "" {
			continue
		}
		if id == def {
			label += "（默认）"
		}
		out = append(out, AlsaDevice{Name: id, Label: label})
	}
	return out
}

// FindECHOADevice Windows 无 ECHO-A/TTGK 概念;把"首选设备"理解为系统默认播放入口,
// 返回其端点 ID(最稳、几乎必能在独占模式驱动)。找不到返回空,让调用方走 ds[0]。
func FindECHOADevice() string {
	comInit()
	var enc unsafe.Pointer
	if err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator, &enc); err != nil {
		return ""
	}
	defer comRelease(enc)
	var dev unsafe.Pointer
	if !hrOK(comCall(enc, 4, eRender, eConsole, ap(&dev))) || dev == nil { // GetDefaultAudioEndpoint
		return ""
	}
	defer comRelease(dev)
	return deviceID(dev)
}

// ---------- 打开输出设备 ----------

// resolveDevice 解析端点 ID 或 "default" 为 IMMDevice 指针(调用方负责 comRelease)。
func resolveDevice(name string) (unsafe.Pointer, error) {
	var enc unsafe.Pointer
	if err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator, &enc); err != nil {
		return nil, err
	}
	defer comRelease(enc)
	var dev unsafe.Pointer
	if name == "" || strings.EqualFold(name, "default") {
		comCall(enc, 4, eRender, eConsole, ap(&dev)) // GetDefaultAudioEndpoint
	} else {
		pw := windows.StringToUTF16Ptr(name)
		comCall(enc, 5, uintptr(unsafe.Pointer(pw)), ap(&dev)) // GetDevice
	}
	if dev == nil {
		return nil, fmt.Errorf("player: 输出设备 %s 不存在或不可用", name)
	}
	return dev, nil
}

// OpenALSA 打开 name 指定的端点。name 为空或 "default" 时用默认播放入口;
// 否则按端点 ID 定位。优先独占事件驱动,独占若不可靠(下溢停钟)自动回退共享(AUTOCONVERTPCM)。
func OpenALSA(name string, sampleRate, channels, bps int) (*ALSA, error) {
	comInit()
	dev, err := resolveDevice(name)
	if err != nil {
		return nil, err
	}
	cands, err := buildCandidates(sampleRate, channels, bps)
	if err != nil {
		comRelease(dev)
		return nil, err
	}
	var lastErr error
	for _, exclusive := range []bool{true, false} {
		a, err := openMode(dev, cands, channels, exclusive)
		if err == nil {
			return a, nil // ALSA 接管 dev 所有权(Close 时释放)
		}
		lastErr = err
	}
	comRelease(dev) // 独占/共享都失败,释放 dev
	return nil, fmt.Errorf("player: 打不开输出设备(独占/共享都失败): %v", lastErr)
}

// openMode 以指定模式打开一个 IAudioClient。独占模式先挑设备原生支持的候选格式,并做
// "能否持续"探测;失败释放本模式的 client/event/render 并返回错误,不释放 dev(所有权在外层)。
func openMode(dev unsafe.Pointer, cands []formatCandidate, channels int, exclusive bool) (*ALSA, error) {
	var client unsafe.Pointer
	fail := func(err error) (*ALSA, error) {
		if client != nil {
			comRelease(client)
		}
		return nil, err
	}
	if !hrOK(comCall(dev, 3, ap(&iidIAudioClient), 0, 0, ap(&client))) || client == nil { // Activate
		return fail(fmt.Errorf("player: Activate IAudioClient 失败"))
	}

	// 选格式:独占挑设备原生支持的候选(以原采样率/位深直出);共享用第一个交给系统换算。
	cand := cands[0]
	if exclusive {
		picked := selectExclusiveFormat(client, cands)
		if picked == nil {
			return fail(fmt.Errorf("player: 设备无原生支持的独占格式"))
		}
		cand = *picked
	}

	ok, _, hr := initStream(client, cand.wfx, exclusive)
	if !ok {
		return fail(fmt.Errorf("player: WASAPI 初始化失败(hr=0x%08x)", hr))
	}

	var bufSize uint32
	comCall(client, 4, ap(&bufSize)) // GetBufferSize
	if bufSize == 0 {
		return fail(fmt.Errorf("player: WASAPI 缓冲区大小为 0"))
	}

	ev, _, _ := pcCreateEventW.Call(0, 0, 0, 0) // 自动重置、初始未触发
	if ev == 0 {
		return fail(fmt.Errorf("player: CreateEventW 失败"))
	}
	comCall(client, 13, ev) // SetEventHandle

	var render unsafe.Pointer
	if !hrOK(comCall(client, 14, ap(&iidIAudioRenderClient), ap(&render))) || render == nil { // GetService
		pcCloseHandle.Call(ev)
		return fail(fmt.Errorf("player: GetService IAudioRenderClient 失败"))
	}

	a := &ALSA{
		client:    client,
		render:    render,
		dev:       dev,
		evt:       ev,
		bufSize:   bufSize,
		bytesPer:  cand.bytesPer,
		shift:     cand.shift,
		frame:     cand.bytesPer * channels,
		exclusive: exclusive,
		started:   false,
	}
	if !exclusive {
		return a, nil
	}
	// 独占:探测能否持续。停滞/超时说明该设备独占不可靠,回退共享(释放本模式的 COM)。
	if !a.probeExclusive(400 * time.Millisecond) {
		pcCloseHandle.Call(ev)
		comRelease(render)
		comRelease(client)
		return nil, fmt.Errorf("player: 独占模式在该设备不可靠(探测停滞/超时)")
	}
	// 独占可用:停流 + Reset 清掉探测写的静音,让引擎首段从头进。
	comCall(client, 11) // Stop
	comCall(client, 12) // Reset(清缓冲)
	a.started = false
	a.fifo = a.fifo[:0]
	setProAudioThread() // MMCSS "Pro Audio" 提线程优先级,降低独占爆音(照 mpv)
	return a, nil
}

// setProAudioThread 把当前线程注册到 MMCSS "Pro Audio" 类别,降低音频爆音概率。
// best-effort:失败静默(不影响功能);不 Revert,线程结束时系统自动回收。
func setProAudioThread() {
	name, err := windows.UTF16PtrFromString("Pro Audio")
	if err != nil {
		return
	}
	var idx uint32
	pcAvSetMmThreadCharacteristicsW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&idx)))
}

// probeExclusive 往刚初始化的独占流写入约 5 个缓冲周期的静音,限定时间内能持续推进则判定
// 独占可维持。返回 false 表示该设备独占不可靠(下溢停钟),调用方应回退共享。
// probeExclusive 用整缓冲模型填 3 个缓冲并 Start,验证设备能否按事件持续消化(独占可持续)。
// 事件超时 / GetBuffer 失败即返回 false,调用方同设备回退共享。
func (a *WASAPI) probeExclusive(timeout time.Duration) bool {
	if a.bufSize == 0 {
		return false
	}
	bufBytes := int(a.bufSize) * a.frame
	deadline := time.Now().Add(timeout)
	// 填满「两个」缓冲(静音)再 Start,避免探测本身播到未初始化缓冲出噪音。
	for i := 0; i < 2; i++ {
		if time.Now().After(deadline) || !a.fillSilence(bufBytes) {
			return false
		}
	}
	comCall(a.client, 10) // Start
	a.started = true
	// 再等一次事件并补一块,验证设备能持续按期消化。
	if time.Now().After(deadline) || !a.waitEvent() {
		return false
	}
	return a.fillSilence(bufBytes)
}

// initStream 尝试以事件驱动方式初始化音频流。独占失败返回 false,交由调用方回退共享。
func initStream(client unsafe.Pointer, wfx *waveFormatExtensible, exclusive bool) (bool, bool, uintptr) {
	if exclusive {
		// 独占事件驱动:Microsoft 规定 hnsPeriodicity 必须非 0 且等于 hnsBufferDuration。
		// 先取设备默认周期;若返回"缓冲大小未对齐"(AUDCLNT_E_BUFFER_SIZE_NOT_ALIGNED),
		// 退到最小周期重试一次(参考 mpv 的 align hack)。
		var defPer, minPer int64
		comCall(client, 9, ap(&defPer), ap(&minPer)) // GetDevicePeriod
		if defPer <= 0 {
			defPer = defaultPeriodHns
		}
		tryInit := func(dur int64) uintptr {
			return comCall(client, 3,
				uintptr(shareModeExclusive), uintptr(streamFlagsEventCallback),
				uintptr(dur), uintptr(dur),
				ap(wfx), 0)
		}
		hr := tryInit(defPer)
		if hr == audclntBufferSizeNotAligned && minPer > 0 && minPer != defPer {
			hr = tryInit(minPer)
		}
		return hrOK(hr), hrOK(hr), hr
	}
	// 共享:AUTOCONVERTPCM 让系统把 file 原生格式换算到端点混音格式;缓冲/周期交给系统(0)。
	// 用 timer-driven(轮询 GetCurrentPadding),不用 EVENTCALLBACK——事件驱动的共享在本机
	// 设备上会出现 GetBuffer 返回 S_OK+空指针 / OUT_OF_ORDER,轮询更稳。
	hr := comCall(client, 3,
		uintptr(shareModeShared), uintptr(streamFlagsAutoConvertPCM),
		0, 0,
		ap(wfx), 0)
	return hrOK(hr), false, hr
}

// ---------- 播放设备实现(接口与 Linux 端一致) ----------

// WASAPI 是一个独占/共享的播放设备(io.Writer:入参为交错整数 PCM 字节)。
type WASAPI struct {
	client    unsafe.Pointer // IAudioClient
	render    unsafe.Pointer // IAudioRenderClient
	dev       unsafe.Pointer // IMMDevice
	evt       uintptr        // HANDLE(事件驱动 pacing)
	bufSize   uint32         // 缓冲帧数
	bytesPer  int            // 每样本容器字节数(1/2/4)
	shift     uint           // 内容左移位数
	frame     int            // 每帧字节 = channels*bytesPer
	exclusive bool
	started   bool   // 是否已 Start(首次提交缓冲后置 true)
	fifo      []byte // 独占模式:攒够一个整缓冲再整块提交(见 writeExclusive)
}

// ALSA 在 Windows 上就是 WASAPI(类型别名,让 engine 的 *player.ALSA 零改动)。
type ALSA = WASAPI

// AppendSample 按容器编码追加一个 int32 样本(小端,左对齐)。
func (a *WASAPI) AppendSample(buf []byte, v int32) []byte {
	return appendPCM(buf, v, a.bytesPer, a.shift)
}

// waitEvent 等设备事件通知;返回 true=被信号唤醒,false=超时(100ms,防事件丢失卡死)。
func (a *WASAPI) waitEvent() bool {
	r, _, _ := pcWaitForSingleObject.Call(a.evt, 100)
	return r == 0 // WAIT_OBJECT_0
}

// Write 阻塞写入交错 PCM,直到全部进入设备缓冲(实现 io.Writer 的节流)。
// 若缓冲一直无空间(设备停滞/独占下溢未恢复),超时返回错误,交由引擎自动换设备。
func (a *WASAPI) Write(p []byte) (n int, err error) {
	// 崩溃防御:com 返回的 HRESULT 可能是 S_OK/S_BUFFER_EMPTY 等"成功但空"、
	// ppData 未填而残留垃圾指针;加一层 recover 把任何 runtime panic 转成错误返回,
	// 绝不让引擎 goroutine 崩掉整个 app(引擎拿到 err 会走自动换设备)。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("player: WASAPI Write 异常(已恢复): %v", r)
		}
	}()

	if len(p) == 0 {
		return 0, nil
	}
	if len(p)%a.frame != 0 {
		return 0, fmt.Errorf("player: WASAPI 输入不是整帧(每帧 %d 字节): %d", a.frame, len(p))
	}
	if a.exclusive {
		return a.writeExclusive(p)
	}
	return a.writeShared(p)
}

// writeShared 共享模式:轮询 GetCurrentPadding,按可用空间部分填(共享只有单一缓冲)。
func (a *WASAPI) writeShared(p []byte) (int, error) {
	off := 0
	lastProgress := time.Now()
	for off < len(p) {
		avail := int(a.bufSize)
		if a.started {
			var pad uint32
			hr := comCall(a.client, 6, ap(&pad)) // GetCurrentPadding
			if !hrOK(hr) {
				return off, fmt.Errorf("player: WASAPI GetCurrentPadding 失败")
			}
			avail = int(a.bufSize) - int(pad)
		}
		if avail <= 0 {
			time.Sleep(2 * time.Millisecond)
			if time.Since(lastProgress) > 4*time.Second {
				return off, fmt.Errorf("player: WASAPI 输出设备停滞(4s 无可用缓冲)")
			}
			continue
		}
		frames := avail
		if remain := (len(p) - off) / a.frame; frames > remain {
			frames = remain
		}
		if frames <= 0 {
			break
		}
		var data unsafe.Pointer
		hr := comCall(a.render, 3, uintptr(frames), ap(&data)) // GetBuffer
		if !hrOK(hr) || data == nil {
			// 瞬态(S_OK+空指针 / BUFFER_OPERATION_PENDING):短暂等待后重试,别当终止。
			time.Sleep(2 * time.Millisecond)
			if time.Since(lastProgress) > 5*time.Second {
				return off, fmt.Errorf("player: WASAPI GetBuffer 持续失败(hr=0x%08x)", hr)
			}
			continue
		}
		nbytes := frames * a.frame
		copy(unsafe.Slice((*byte)(data), nbytes), p[off:off+nbytes])
		comCall(a.render, 4, uintptr(frames), 0) // ReleaseBuffer(flags=0)
		if !a.started {
			comCall(a.client, 10) // Start
			a.started = true
		}
		off += nbytes
		lastProgress = time.Now()
	}
	return len(p), nil
}

// writeExclusive 独占事件驱动喂法(照 mpv ao_wasapi):WASAPI 为独占流分配「两个」各
// bufSize 帧的缓冲,事件 ping-pong 交换。必须每次 GetBuffer(bufSize) 请求并填「整个缓冲」,
// 且只在设备信号事件后才填下一块(独占的 GetCurrentPadding 跨两个缓冲计,用它算空间会得 0)。
// 关键:Start 前必须把「两个」缓冲都填上——否则设备播到第二个未填缓冲会读出未初始化内存,
// 表现为切歌/快进/起播瞬间的"滋滋"噪音。
func (a *WASAPI) writeExclusive(p []byte) (int, error) {
	if a.bufSize == 0 {
		return 0, fmt.Errorf("player: WASAPI 缓冲区大小为 0")
	}
	a.fifo = append(a.fifo, p...)
	bufBytes := int(a.bufSize) * a.frame
	lastProgress := time.Now()

	// 首块:攒够一个整缓冲后,先把两个 ping-pong 缓冲都填上再 Start。
	if !a.started {
		if len(a.fifo) < bufBytes {
			return len(p), nil // 还不够一个整块,等后续 Write 补
		}
		if !a.fillOne(bufBytes) {
			return 0, fmt.Errorf("player: WASAPI 独占首块填充失败")
		}
		// 第二个缓冲:有整块数据就填数据,没有就填整块静音(不混半块、不留未初始化)。
		if len(a.fifo) >= bufBytes {
			if !a.fillOne(bufBytes) {
				return 0, fmt.Errorf("player: WASAPI 独占次块填充失败")
			}
		} else if !a.fillSilence(bufBytes) {
			return 0, fmt.Errorf("player: WASAPI 独占静音填充失败")
		}
		comCall(a.client, 10) // Start
		a.started = true
	}

	// 之后:纯事件驱动——等设备消化掉一块,然后尽快补齐(每事件最多填两块,照 mpv
	// `thread_feed() && thread_feed()`)。绝不用 GetCurrentPadding 决定是否可填:独占下它
	// 不可靠,会导致 GetBuffer 拿到仍在播放的缓冲并覆盖它 → 怪声/错位。GetBuffer 只会返回
	// 空闲缓冲,两块都满时它会失败——那是正常信号,直接停手等下一个事件。
	for len(a.fifo) >= bufBytes {
		if !a.waitEvent() {
			if debugWASAPI != "" {
				fmt.Fprintf(os.Stderr, "[wapi] 事件超时 fifo=%d\n", len(a.fifo))
			}
			comCall(a.client, 10) // 下溢停钟:唤醒重试
			if time.Since(lastProgress) > 5*time.Second {
				return 0, fmt.Errorf("player: WASAPI 独占事件超时(设备停滞)")
			}
			continue
		}
		if !a.fillOne(bufBytes) {
			// 事件到了却没填空闲缓冲 → 独占流已失序/停钟(GetBuffer 返回 OUT_OF_ORDER)。
			// 光 Start 不顶用,做一次 Stop+Reset 复位,重填两块再 Start,让播放续上。
			if debugWASAPI != "" {
				fmt.Fprintf(os.Stderr, "[wapi] 流失序,复位重来 fifo=%d\n", len(a.fifo))
			}
			comCall(a.client, 11) // Stop
			comCall(a.client, 12) // Reset
			for i := 0; i < 2; i++ {
				if len(a.fifo) >= bufBytes {
					a.fillOne(bufBytes)
				} else {
					a.fillSilence(bufBytes)
				}
			}
			comCall(a.client, 10) // Start
			a.started = true
			if time.Since(lastProgress) > 5*time.Second {
				return 0, fmt.Errorf("player: WASAPI 独占复位后仍失败(设备停滞)")
			}
			lastProgress = time.Now()
			continue
		}
		lastProgress = time.Now()
	}
	return len(p), nil
}

// fillOne 取一个整缓冲,从 fifo 拷满 bufBytes 字节后 ReleaseBuffer。fifo 不足整块时返回
// false(不取缓冲、不释放,避免泄漏)。调用方保证 fifo 够。
func (a *WASAPI) fillOne(bufBytes int) bool {
	if len(a.fifo) < bufBytes {
		return false
	}
	var data unsafe.Pointer
	if !hrOK(comCall(a.render, 3, uintptr(a.bufSize), ap(&data))) || data == nil {
		return false
	}
	copy(unsafe.Slice((*byte)(data), bufBytes), a.fifo[:bufBytes])
	a.fifo = a.fifo[bufBytes:]
	comCall(a.render, 4, uintptr(a.bufSize), 0) // ReleaseBuffer(整个缓冲)
	return true
}

// fillSilence 取一个整缓冲填静音后 ReleaseBuffer(起播时第二个缓冲没数据可用)。
func (a *WASAPI) fillSilence(bufBytes int) bool {
	var data unsafe.Pointer
	if !hrOK(comCall(a.render, 3, uintptr(a.bufSize), ap(&data))) || data == nil {
		return false
	}
	clear(unsafe.Slice((*byte)(data), bufBytes))
	comCall(a.render, 4, uintptr(a.bufSize), 0) // ReleaseBuffer
	return true
}

// Pause 暂停/继续。引擎暂停时本就停止喂数据,这里用 Stop/Start 复位流,
// 最稳(避免独占下溢卡死);恢复有极小间隙,可接受。
func (a *WASAPI) Pause(on bool) error {
	if on {
		comCall(a.client, 11) // IAudioClient::Stop
	} else {
		comCall(a.client, 10) // IAudioClient::Start
	}
	return nil
}

// Close 停止流并释放所有 COM 引用。独占下先等缓冲放完,避免掐掉尾部。
func (a *WASAPI) Close() error {
	if a.client == nil {
		return nil
	}
	if a.exclusive && a.started {
		a.drainExclusive()
	}
	comCall(a.client, 11) // Stop
	if a.evt != 0 {
		pcCloseHandle.Call(a.evt)
	}
	comRelease(a.render)
	comRelease(a.client)
	comRelease(a.dev)
	a.client, a.render, a.dev, a.evt = nil, nil, nil, 0
	a.fifo = nil
	return nil
}

// drainExclusive 独占:等设备把已提交的缓冲放完(pad 归零),最多约 300ms。
func (a *WASAPI) drainExclusive() {
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		var pad uint32
		if !hrOK(comCall(a.client, 6, ap(&pad))) || pad == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ProbeChannels Windows 探测声道范围;engine 未实际用到,留 API 完整。
func ProbeChannels(name string) (int, int, error) {
	return 1, 8, nil
}

// Exclusive 当前流是否独占(位完美直出)模式,供 UI 显示。
func (a *WASAPI) Exclusive() bool { return a.exclusive }

// DeviceMixFormat 返回指定端点(空/"default"→默认播放入口)的共享混音格式:采样率/容器位深/声道。
// 用于 bit-perfect 检测:共享模式下 WASAPI 最终是混到该格式(重采样/升位都发生在这里),
// 拿它和 FLAC 的 STREAMINFO 比对,即可判断有没有被处理。
func DeviceMixFormat(name string) (rate, bits, ch int, ok bool) {
	comInit()
	dev, err := resolveDevice(name)
	if err != nil {
		return 0, 0, 0, false
	}
	defer comRelease(dev)
	var client unsafe.Pointer
	if !hrOK(comCall(dev, 3, ap(&iidIAudioClient), 0, 0, ap(&client))) || client == nil { // Activate
		return 0, 0, 0, false
	}
	defer comRelease(client)
	var mf unsafe.Pointer
	hr := comCall(client, 8, ap(&mf)) // GetMixFormat
	if !hrOK(hr) || mf == nil {
		return 0, 0, 0, false
	}
	defer windows.CoTaskMemFree(mf)
	w := (*waveFormatEx)(mf)
	return int(w.nSamplesPerSec), int(w.wBitsPerSample), int(w.nChannels), true
}
