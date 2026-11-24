# SMB 网络盘

在 Mac mini 上创建一个用于协作的外置盘目录。将网页配置中的 `workspace.path` 和 SMB 共享指向同一目录，这样网页、Finder 和 Windows 就能看到同一批文件。

## 启用共享

- 可以用 macOS 的「系统设置 → 通用 → 共享 → 文件共享」，或者用 Samba 容器。
- 只共享指定目录。给需要访问的用户分配 SMB 登录账号和「读与写」权限。Tailscale 用户身份和 SMB 登录账号分别管理。
- 使用 Docker Samba 时，将外置盘共享目录挂到容器，使用 `127.0.0.1:1445:445` 端口映射，关闭 guest 访问。用 `tailscale serve --bg --tcp=445 tcp://127.0.0.1:1445` 在 tailnet 内提供 SMB。确认代理前没有同端口冲突，保持公网不可达。
- 在 Tailscale 管理界面检查端口 445 的访问规则，确保只有获授权的成员或设备可以连接。始终保留 Samba 本身的认证。

## 从客户端连接

- macOS Finder 按 Cmd+K，输入 `smb://<主机的 Tailscale 名称>/<共享名>`。
- Windows 可在文件资源管理器中输入 `\\<主机的 Tailscale 地址>\<共享名>`，或映射成盘符。
- iPhone 可以在「文件 → 浏览 → 连接服务器」中输入同一个 SMB 地址。

以设置好的 SMB 账号登录。读写会直接作用于 Mac mini 外置盘上的共享目录，Mac mini 和外置盘需要在线。

## Docker Compose 示例

[`examples/samba`](../examples/samba) 提供一个共享目录和一个 SMB 账号 `sharedisk`。从该目录执行以下命令。启动前用 `diskutil info /Volumes/ExternalDisk` 确认外置卷确实已挂载，并将路径替换成网页同一共享目录。

```bash
export SMB_SHARE_PATH='/Volumes/ExternalDisk/Device Files/Shared'
export HOST_UID="$(id -u)"
docker compose up -d --build
docker compose exec samba smbpasswd -a sharedisk
tailscale serve --bg --tcp=445 tcp://127.0.0.1:1445
```

`smbpasswd` 会请求设置账号密码。连接时使用共享名 `Shared`。Docker Desktop 如果提示文件访问或卷共享权限，只授权指定外置目录。拔盘前停止该 Samba 服务，重新挂载确认后再启动。
