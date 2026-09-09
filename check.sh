#!/usr/bin/env bash
# 检测"当前正在播的 FLAC"与"音频设备实际格式"是否逐位一致(直出/重采样/降位)。
# 用法:./check.sh [flaccheck 二进制路径]
# 说明:flaccheck 自己会从 ~/.config/flacplayer/state.json 读正在播的歌与设备
#       (Linux 取 /proc/asound,Windows 取 GetMixFormat);需先播放过一次。
set -euo pipefail

STATE="$HOME/.config/flacplayer/state.json"
BIN="${1:-$PWD/flaccheck}"

if [ ! -s "$STATE" ]; then
  echo "没有状态文件 $STATE —— 先运行过一次 flacplayer(至少播放过一次)才会生成。" >&2
  exit 2
fi

if [ ! -x "$BIN" ]; then
  echo "编译检测工具 → $BIN"
  go build -o "$BIN" ./tools/check
fi

exec "$BIN"
