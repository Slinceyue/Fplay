# FlacPlayer (Fplay)

一个用 Go 写的 FLAC 播放器。两条分支各是一个形态:

- **`main`** — 桌面终端 TUI 播放器(GNOME/Kitty 等真终端里用)
- **`feature/embedded`** — 无桌面嵌入式/无头播放器(stdin 命令 + stdout JSON 事件,systemd 自启,面向自研 PCB)

## 亮点

- **ALSA 直写**(Linux):`purego` 动态加载 libasound,0 CGO;逐位直出、精确采样率、拒绝重采样
- **24bit 用 S32_LE 容器**(内容左对齐),规避 3 字节 S24 在部分 USB DAC/plughw 上的错位白噪
- **Seek 表磁盘缓存**:同一首歌 Seek 只建一次表,之后秒跳、跨进程复用
- **播放时长可信**:进度直接取引擎真实位置,不用墙钟推算
- 单实例锁、备用屏幕、鼠标滚轮一格一格、媒体键(evdev)、MPRIS 顶栏媒体卡、退出自动恢复系统声音

## 依赖

Go 1.26+;主要依赖:`mewkiz/flac`(解码)、`ebitengine/purego`(ALSA)、`godbus/dbus`、`coreos/go-systemd`、`golang.org/x/term`。

## 构建 / 运行

```bash
# 一键:lint / 单测 / 交叉编译(linux amd64、linux arm64)
make lint && make test && make cross

# 桌面版(建议装到 PATH 后敲 Fplay)
make install
Fplay

# 交叉产物
build/linux-amd64/Fplay
build/linux-arm64/Fplay      # 嵌入式主目标
```
> Windows 版在独立 **win** 分支(桌面版;声音后端 WASAPI 待实现)。

## 桌面版(main)操作

```
Enter 播放 · Space 暂停 · t 列表 · n/b 切歌
← → 快退/快进(±10s) · ]/[ 音量 · v 静音
Home/End/PgUp/PgDn 列表翻页 · m 播放模式
Ctrl+Shift+= / Ctrl+- 终端字号 · l 歌词 · f 目录 · o 输出设备 · q 退出
```

播放模式:顺序 / 随机 / 单曲循环 / 播放单曲。歌词大屏当前句垂直居中,支持封面元数据与 MPRIS。

## 嵌入式/无头版(feature/embedded)

运行形态:`stdin` 一行一命令,播放状态以 **JSON 行**输出到 `stdout`,方便接任意前端(LCD/Web/触屏)。

```bash
# 命令
list | play <id> | pause | next | prev | vol <0..150> | seek <秒> | mode <seq|single|rand|repeat>
cover <id>      # 抽内嵌封面到临时文件
lyrics <id> [秒] # 歌词全文 + 当前行(同名 .lrc)
status | quit
# 事件: ready / list / song / tick / state / lyrics / cover / error / rescan
```

特性:递归扫描 `.flac` 读 TITLE/ARTIST;目录 `-lib`/`FPLAY_LIB`/`~/music` + 挂载点 music,目录持久化、U盘/SD 插拔自动重扫;媒体键(evdev)。

服务化(自启 + 看门狗):

```bash
make install-service-run
journalctl -u fplay -f
# contrib/fplay.service:Type=notify, WatchdogSec=30(程序 10s 喂一次)
```

## 测试 / 验证

```bash
make test          # 单元 + 真实 flac 集成冒烟
make test-race
# 桌面逐位直出验证(播放中另开终端):
./check.sh          # PASS=无重采样/无降位
```

## 分支与路线

- `main`:稳定桌面版(当前 HEAD 基线)
- `feature/embedded`:无头/嵌入式开发中(声卡已虚拟接入 Debian 12 模拟器验证)

后续:嵌入式 LCD/触屏前端、真 PCB(arm64)联调;Windows(桌面)在 `win` 分支做 WASAPI 出声。

## License

未指定(保留作者权利);商用前请联系作者。
