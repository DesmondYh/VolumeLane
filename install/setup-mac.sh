#!/bin/bash
set -euo pipefail
umask 077

fail() { printf '设备文件：%s\n' "$*" >&2; exit 1; }

[[ $# == 0 ]] || fail '用法：bash install/setup-mac.sh'
[[ $(uname -s) == Darwin && $(id -u) != 0 ]] || fail '请在已登录的 macOS 用户下运行，避免使用 sudo。'
case $(uname -m) in
  arm64) agent_arch=arm64 ;;
  x86_64) agent_arch=amd64 ;;
  *) fail '需要 Apple 芯片或 Intel 芯片的 Mac。' ;;
esac

repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
[[ -r "$repo/compose.yaml" ]] || fail '请从完整项目目录运行安装脚本。'
docker_cli=$(command -v docker || true)
docker_cli=${docker_cli:-/Applications/Docker.app/Contents/Resources/bin/docker}
[[ -x "$docker_cli" ]] || fail '请先安装并启动 Docker Desktop。'
context=$("$docker_cli" context show)
endpoint=$("$docker_cli" context inspect --format '{{ (index .Endpoints "docker").Host }}' "$context")
docker_local=("$docker_cli" --context "$context")
[[ $endpoint == unix://* ]] || fail '请切换到本机 Docker Desktop 的 Docker context。'
[[ $("${docker_local[@]}" info --format '{{.OperatingSystem}}' 2>/dev/null) == 'Docker Desktop' ]] || fail '请先启动本机 Docker Desktop。'
"${docker_local[@]}" compose version >/dev/null || fail '需要 Docker Compose。'
compose=("${docker_local[@]}" compose -p device-files -f "$repo/compose.yaml")

tailscale=$(command -v tailscale || true)
tailscale=${tailscale:-/Applications/Tailscale.app/Contents/MacOS/Tailscale}
[[ -x "$tailscale" ]] || fail '请先安装 Tailscale 并登录。'
[[ -x /usr/bin/ruby ]] || fail '无法找到 macOS 自带的 Ruby。'
domain="gui/$(id -u)"
launchctl print "$domain" >/dev/null 2>&1 || fail '请先在这台 Mac 上登录桌面，再运行安装。'

support="$HOME/Library/Application Support"
app="$support/Device Files"
label=local.device-files.agent
plist="$HOME/Library/LaunchAgents/$label.plist"
job="$domain/$label"
volume=device-files_state
origin=http://127.0.0.1:18766
agent="$app/device-files"
marker="$app/.managed-setup"

mkdir -p "$support"
lock="$support/.device-files-setup.lock"
mkdir "$lock" 2>/dev/null || fail '另一次安装正在运行，或上次异常退出后留下了安装锁。'
artifact=
cleanup() {
  if [[ -n $artifact ]]; then "${docker_local[@]}" rm "$artifact" >/dev/null 2>&1 || true; fi
  rm -f "$lock/status.json" "$lock/serve.json" "$lock/identity" "$lock/volume.json" "$lock/device-files" "$lock/agent.plist"
  rmdir "$lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ -L "$app" || -L "$marker" || -L "$plist" ]]; then fail '发现指向其他位置的现有安装文件，安装已停止。'; fi
if [[ ! -f "$marker" ]]; then
  if [[ -e "$app" || -e "$plist" ]] || launchctl print "$job" >/dev/null 2>&1 ||
    "${docker_local[@]}" volume inspect "$volume" >/dev/null 2>&1 ||
    [[ -n $("${compose[@]}" ps -aq) ]]; then
    fail '已发现设备文件的现有安装。请保留其配置并按手动部署说明维护。'
  fi
  for port in 18766 18776; do
    if nc -G 1 -z 127.0.0.1 "$port" >/dev/null 2>&1; then fail "本机端口 $port 已被占用。"; fi
  done
fi

"$tailscale" status --json > "$lock/status.json" || fail '请登录并连接 Tailscale。'
"$tailscale" serve status --json > "$lock/serve.json" || fail '无法检查 Tailscale Serve。'
/usr/bin/ruby - "$lock/status.json" "$lock/serve.json" "$origin" > "$lock/identity" <<'RUBY'
require 'json'
status = JSON.parse(File.read(ARGV[0]))
self_node = status['Self'] || {}
login = status.fetch('User', {}).fetch(self_node['UserID'].to_s, {})['LoginName']
node = self_node['ID']
host = self_node['DNSName'].to_s.sub(/\.$/, '')
unless status['BackendState'] == 'Running' && Array(self_node['Tags']).empty? && login && node &&
       !login.empty? && !node.empty? && host.match?(/\A[a-zA-Z0-9][a-zA-Z0-9.-]+\z/)
  abort '设备文件：请连接已启用 MagicDNS 的 Tailscale 网络，并使用个人身份登录这台 Mac。'
end

payload = File.read(ARGV[1]).strip
serve = payload.empty? ? {} : JSON.parse(payload)
serve ||= {}
port = '8766'
address = "#{host}:#{port}"
proxy = serve.dig('Web', address, 'Handlers', '/', 'Proxy')
ready = serve.dig('TCP', port, 'HTTP') == true && proxy == ARGV[2] && !serve.dig('AllowFunnel', address)
foreground = (serve['Foreground'] || {}).values
occupied = ([serve] + foreground).any? { |config| config.dig('TCP', port) }
if occupied && !ready
  abort '设备文件：Tailscale 端口 8766 已被其他服务占用，现有路由已保留。'
end
puts JSON.generate(login: login, node: node, host: host, existing_route: ready)
RUBY

if [[ ! -f "$marker" ]]; then
  mkdir -m 700 "$app"
  : > "$marker"
fi
[[ -d "$app" && -O "$app" ]] || fail '设备文件的配置目录不可访问。'
for file in "$app/agent.json" "$app/hub.json" "$agent"; do
  [[ ! -L "$file" ]] || fail '现有配置或程序指向其他位置，请检查后再运行。'
done
if [[ -f "$plist" ]]; then
  expected=("$agent" agent "$app/agent.json" 127.0.0.1:18776)
  for index in 0 1 2 3; do
    [[ $(plutil -extract "ProgramArguments.$index" raw -o - "$plist") == "${expected[$index]}" ]] ||
      fail '现有接入服务的启动参数不同，请保留并检查原先配置。'
  done
fi

/usr/bin/ruby - "$lock/identity" "$app" <<'RUBY'
require 'json'
require 'securerandom'
require 'tempfile'

def store(path, value)
  Tempfile.create('.device-files', File.dirname(path)) do |temporary|
    temporary.write(JSON.pretty_generate(value) + "\n")
    temporary.close
    File.rename(temporary.path, path)
  end
end

identity = JSON.parse(File.read(ARGV[0]))
agent_path = File.join(ARGV[1], 'agent.json')
hub_path = File.join(ARGV[1], 'hub.json')
hub = File.exist?(hub_path) ? JSON.parse(File.read(hub_path)) : nil
agent = File.exist?(agent_path) ? JSON.parse(File.read(agent_path)) : nil
token = (agent && agent['token']) || (hub && hub['localToken']) || SecureRandom.hex(32)
unless token.is_a?(String) && token.match?(/\A[A-Za-z0-9_-]{32,}\z/) &&
       (!agent || agent['listTailnet']) &&
       (!hub || (hub['login'] == identity['login'] && hub['localID'] == identity['node'] && hub['localToken'] == token))
  abort '设备文件：现有配置的 Tailscale 身份或凭据不匹配，配置已保留。'
end
store(agent_path, { token: token, listTailnet: true }) unless agent
store(hub_path, { login: identity['login'], localID: identity['node'], localToken: token, remotes: {} }) unless hub
RUBY

printf '%s\n' '正在构建网页服务和接入工具。'
"${compose[@]}" build web
artifact=$("${docker_local[@]}" create device-files:local)
"${docker_local[@]}" cp "$artifact:/app/install/device-files-$agent_arch" "$lock/device-files"
"${docker_local[@]}" rm "$artifact" >/dev/null
artifact=

"${docker_local[@]}" volume create --label com.docker.compose.project=device-files \
  --label com.docker.compose.volume=state "$volume" >/dev/null
"${docker_local[@]}" run --rm -i --no-healthcheck --network none --user 0:0 --read-only \
  -v "$volume:/state" --entrypoint sh device-files:local -e -c '
    if [ ! -f /state/hub.json ]; then
      umask 077
      tmp=$(mktemp /state/.hub.XXXXXX)
      trap '\''rm -f "$tmp"'\'' EXIT
      cat > "$tmp"
      chown 65534:65534 "$tmp"
      mv "$tmp" /state/hub.json
    fi
    cat /state/hub.json
  ' < "$app/hub.json" > "$lock/volume.json"
/usr/bin/ruby -rjson -e '
  local, active = ARGV.map { |path| JSON.parse(File.read(path)) }
  unless %w[login localID localToken].all? { |key| local[key] == active[key] }
    abort "设备文件：Docker 中的登录身份或令牌与本机配置不同，现有 Docker 配置已保留。"
  end
' "$app/hub.json" "$lock/volume.json"

changed=false
if ! cmp -s "$lock/device-files" "$agent"; then
  chmod 700 "$lock/device-files"
  mv "$lock/device-files" "$agent"
  changed=true
fi
if [[ ! -f "$plist" ]]; then
  mkdir -p "$(dirname -- "$plist")"
  cat > "$lock/agent.plist" <<'PLIST'
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
  plutil -insert ProgramArguments.0 -string "$agent" "$lock/agent.plist"
  plutil -insert ProgramArguments.1 -string agent "$lock/agent.plist"
  plutil -insert ProgramArguments.2 -string "$app/agent.json" "$lock/agent.plist"
  plutil -insert ProgramArguments.3 -string 127.0.0.1:18776 "$lock/agent.plist"
  plutil -insert EnvironmentVariables.HOME -string "$HOME" "$lock/agent.plist"
  plutil -insert EnvironmentVariables.PATH -string "$(dirname -- "$tailscale"):/usr/bin:/bin:/usr/sbin:/sbin" "$lock/agent.plist"
  mv "$lock/agent.plist" "$plist"
fi

if launchctl print "$job" >/dev/null 2>&1; then
  if [[ $changed == true ]]; then launchctl kickstart -k "$job"; fi
else
  launchctl bootstrap "$domain" "$plist"
fi
"${compose[@]}" up -d web

health() {
  local code
  for attempt in {1..12}; do
    code=$(curl --noproxy '*' --max-time 2 -s -o /dev/null -w '%{http_code}' "$1" || true)
    if [[ $code == "$2" ]]; then return 0; fi
    sleep 1
  done
  return 1
}

health "$origin/healthz" 204 || fail 'Docker 网页服务未就绪。请检查 docker compose logs web。'
health "$origin/api/devices" 403 || fail '本机网页服务未拒绝无身份请求，安装已停止。'
host=$(plutil -extract host raw -o - "$lock/identity")
existing=$(plutil -extract existing_route raw -o - "$lock/identity")
if [[ $existing != true ]]; then
  "$tailscale" serve --bg --http=8766 "$origin" >/dev/null || fail '无法开通 Tailscale Serve。'
fi
url="http://$host:8766"
health "$url/api/devices" 200 || fail "无法验证 Tailscale 网页入口 $url 。请检查本机的 MagicDNS、Tailscale 权限与接入服务。"
printf '\n%s\n%s\n' "设备文件已就绪：$url" '在同一 Tailscale 网络上用安装时的账号打开此地址。'
