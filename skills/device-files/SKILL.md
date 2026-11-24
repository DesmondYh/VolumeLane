---
name: device-files
description: Install or troubleshoot Device Files, a self-hosted Mac and Windows file dashboard over Docker and Tailscale. Use for its Mac host setup, device pairing, external workspace, SMB share, or latency checks.
---

# Device Files

## Deploy the Mac host

1. Run on the Mac that will host Device Files, under the signed-in desktop user. Reuse the user’s checkout when available. If the Skill was installed separately, clone `https://github.com/DesmondYh/device-files.git` into a suitable local folder. Use the official GitHub source archive for `main` if Git is unavailable.
2. Verify Docker Desktop is running and Tailscale is signed in with a personal identity in a tailnet with MagicDNS enabled. Let the user complete sign-in or macOS folder permission prompts if they appear.
3. From the repository root, inspect and run `bash install/setup-mac.sh`. It builds the Docker hub, starts the host agent with `launchd`, configures Tailscale Serve, and checks the private endpoint. Preserve existing services if setup reports a conflict. For an already installed host, verify its current endpoint before proposing an update.
4. Return the verified Tailscale URL. When available, check the dashboard shows the host and its disks. The web origin trusts identity forwarded by Tailscale Serve. Keep the origin loopback-bound and retain the configured owner login.

## Add a Mac or Windows device

- Identify the devices the user asks to pair through `tailscale status --json`. Use each selected peer’s `ID` as a key in `remotes`, preserving existing entries. Create a distinct random token of at least 32 characters for each new entry. See `examples/hub.example.json` for its structure.
- On a Docker install, the active hub configuration is `/state/hub.json` in the `device-files_state` volume. Read and update this copy through Docker. Preserve the owner, local token, existing device tokens, and unrelated settings. Validate the merged JSON and use an atomic replacement in the volume that retains UID/GID `65534:65534` and mode `0600`. Keep a private backup and keep `~/Library/Application Support/Device Files/hub.json` consistent when that file is maintained as a local copy. Restart only the Device Files web service to load the change.
- In the dashboard, use the matching device’s private installer. Download and run `Install.command` on a Mac or `Install.bat` on Windows. These ZIPs contain credentials. Verify the device’s storage and a directory listing after it connects.

## Add an external workspace or SMB share

- Confirm the actual external-volume mount point, UUID, free capacity, and write access. Set `workspace` in the host’s `agent.json` with `mount`, `uuid`, and a `path` relative to the volume root. Use a dedicated directory and restart the Device Files host agent. Verify the directory is on the external volume before writing to it.
- For an SMB network drive, use `docs/smb.md`. Point SMB at the same directory as the web workspace, require an authenticated SMB user or group, and keep access within the tailnet. Verify a disposable file created via SMB appears in the dashboard, then remove the test file and confirm both views update.

## Troubleshoot

Check Tailscale reachability, service listeners and routing, authentication, then filesystem permissions. A dashboard that reads but rejects writes can indicate an older agent. For a blank page, compare the direct Serve URL with any configured proxy path. When measuring performance, record Tailscale round-trip delay and whether it is direct or relayed, then time directory listing or a small test transfer separately.
