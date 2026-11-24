$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$serviceDir = Join-Path $env:LOCALAPPDATA 'Device Files'
New-Item -ItemType Directory -Force -Path $serviceDir | Out-Null

$exe = Join-Path $serviceDir 'device-files.exe'
$config = Join-Path $serviceDir 'agent.json'
& taskkill /F /IM device-files.exe 2>$null | Out-Null
Start-Sleep -Milliseconds 800
for ($attempt = 0; $attempt -lt 5; $attempt++) {
  try {
    Copy-Item (Join-Path $here 'device-files.exe') $exe -Force -ErrorAction Stop
    break
  } catch {
    if ($attempt -eq 4) { throw }
    & taskkill /F /IM device-files.exe 2>$null | Out-Null
    Start-Sleep -Milliseconds 800
  }
}
Copy-Item (Join-Path $here 'agent.json') $config -Force

$tailscale = 'C:\Program Files\Tailscale\tailscale.exe'
if (-not (Test-Path $tailscale)) { $tailscale = 'C:\Program Files (x86)\Tailscale\tailscale.exe' }
if (-not (Test-Path $tailscale)) {
  $cmd = Get-Command tailscale.exe -ErrorAction SilentlyContinue
  if ($cmd) { $tailscale = $cmd.Source }
}
if (-not (Test-Path $tailscale)) {
  Write-Host '请先安装并打开 Tailscale，连接与你的 Mac mini 相同的网络。'
  exit 1
}

$startup = [Environment]::GetFolderPath('Startup')
$shortcutPath = Join-Path $startup 'Device Files Agent.lnk'
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $exe
$shortcut.Arguments = 'agent "' + $config + '" 127.0.0.1:18776'
$shortcut.WorkingDirectory = $serviceDir
$shortcut.WindowStyle = 7
$shortcut.Save()

Start-Process -FilePath $exe -ArgumentList ('agent "{0}" 127.0.0.1:18776' -f $config) -WindowStyle Hidden
Start-Sleep -Seconds 2
& $tailscale serve --bg --http=8776 http://127.0.0.1:18776

$desktop = [Environment]::GetFolderPath('Desktop')
Set-Content -Path (Join-Path $desktop '设备文件.url') -Value "[InternetShortcut]`r`nURL=http://127.0.0.1:18766" -Encoding ASCII

Write-Host ''
Write-Host '接入已启动。桌面已生成「设备文件」快捷方式，开梯子时也可以用它打开。'
Write-Host '如果 Windows 询问是否允许此程序运行，请选择允许。'