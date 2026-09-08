#!/usr/bin/env bash
# 检测"当前正在播的 FLAC"与"ALSA 设备实际格式"是否逐位一致(直出/重采样/降位)。
# 用法:./check.sh [flaccheck 二进制路径]
# 说明:从 ~/.config/flacplayer/state.json 自动取正在播的歌与输出设备;
#       需在 flacplayer 播放过程中,另开一个终端运行。
set -euo pipefail

STATE="$HOME/.config/flacplayer/state.json"
BIN="${1:-$PWD/flaccheck}"

if [ ! -s "$STATE" ]; then
  echo "没有状态文件 $STATE —— 先运行过一次 flacplayer(至少播放过一次)才会生成。" >&2
  exit 2
fi

read -r CUR < <(python3 -c "import json;print(json.load(open('$STATE')).get('current') or '')")
read -r DEV < <(python3 -c "import json;print(json.load(open('$STATE')).get('device') or '')")

if [ -z "$CUR" ]; then
  echo "状态里没有'正在播的歌'。请先在 flacplayer 里播放一首,再运行本脚本。" >&2
  exit 2
fi

if [ ! -x "$BIN" ]; then
  echo "编译检测工具 → $BIN"
  go build -o "$BIN" ./tools/check
fi

if [ -z "$DEV" ]; then
  exec "$BIN" -flac "$CUR"
fi
exec "$BIN" -flac "$CUR" -dev "$DEV"
