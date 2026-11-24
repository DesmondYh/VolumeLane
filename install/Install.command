#!/bin/bash
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "$0")" && pwd)
service_dir="$HOME/Library/Application Support/Device Files"
label=local.device-files.agent
plist="$HOME/Library/LaunchAgents/$label.plist"
domain="gui/$(id -u)"
tailscale=/Applications/Tailscale.app/Contents/MacOS/Tailscale
if [[ ! -x "$tailscale" ]]; then tailscale=$(command -v tailscale || true); fi
if [[ -z "$tailscale" ]]; then
  printf '%s\n' '请先打开 Tailscale，连接与你的 Mac mini 相同的网络。'
  exit 1
fi
if ! { "$here/device-files" 2>&1 || true; } | grep -q 'usage: device-files'; then
  printf '%s\n' '无法运行接入工具，请确认下载的版本与你的 Mac 芯片相符。'
  exit 1
fi
mkdir -p "$service_dir" "$HOME/Library/LaunchAgents"
chmod 700 "$service_dir"
install -m 700 "$here/device-files" "$service_dir/device-files"
install -m 600 "$here/agent.json" "$service_dir/agent.json"
if launchctl print "$domain/$label" >/dev/null 2>&1; then launchctl bootout "$domain/$label"; fi
cat > "$plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>local.device-files.agent</string>
  <key>ProgramArguments</key><array></array>
  <key>EnvironmentVariables</key><dict></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
</dict></plist>
PLIST
plutil -insert ProgramArguments.0 -string "$service_dir/device-files" "$plist"
plutil -insert ProgramArguments.1 -string agent "$plist"
plutil -insert ProgramArguments.2 -string "$service_dir/agent.json" "$plist"
plutil -insert ProgramArguments.3 -string 127.0.0.1:18776 "$plist"
plutil -insert EnvironmentVariables.HOME -string "$HOME" "$plist"
launchctl bootstrap "$domain" "$plist"
"$tailscale" serve --bg --http=8776 http://127.0.0.1:18776
printf '\n%s\n' '接入已启动。回到 Mac mini 的网页，选择 MacBook Air 并刷新。'
printf '%s\n' '如果 macOS 询问下载、文稿或外置卷访问权限，请按你希望共享的范围选择。'
