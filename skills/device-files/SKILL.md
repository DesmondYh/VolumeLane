---
name: device-files
description: Deploy or troubleshoot Device Files, a Docker and Tailscale storage dashboard with macOS and Windows agents and an optional writable SMB share. Use for this project's setup, file operations, and latency checks.
---

# Device Files

Locate the Device Files checkout. Use its root `README.md` for setup, `examples/agent.example.json` and `examples/hub.example.json` for local configuration, and `docs/smb.md` when a user wants a network drive. If this skill is installed separately, locate the source checkout or use the repository the user provided.

## Deploy or update

- Identify the host and agents through `tailscale status --json`. Check existing listeners and Tailscale Serve routes before changing ports. The defaults are `127.0.0.1:18766` for the Docker web origin, `127.0.0.1:18776` for the host agent, and Tailscale HTTP port `8766` for users. Preserve other routes when adding or updating this service.
- The web origin trusts the identity header supplied by Tailscale Serve. Keep the origin loopback-bound and configure `login` for the authorized owner. The hub-to-agent bearer tokens are separate from that identity. Keep live configurations and generated per-agent installer ZIPs private.
- For the shared workspace, confirm the external volume's actual mount point, UUID, free capacity and write status. Point the agent's `workspace` and the SMB share to the same dedicated directory. Apply the user's storage preferences. If the external disk is absent, stop before creating or writing to what could be an internal fallback path.

## Share or troubleshoot

- Use authenticated SMB for authorized collaborators. The SMB account or group grants read/write permission to the dedicated share, while Tailscale rules govern network reachability. Sharing the SMB directory does not authorize expanding access to the owner's web dashboard or other disks.
- Verify that a disposable file created through SMB appears in the web workspace, that an SMB read returns its original bytes, and that deleting that test file updates both views. Confirm the actual path is on the external volume. Distinguish browser capabilities from SMB client behavior in the result.
- When access fails, test in order: Tailscale reachability, service listening and routing, authentication, then filesystem permissions. A dashboard that reads but rejects writes can indicate an older agent. For a browser blank page, compare the direct endpoint with the configured proxy path before changing network settings.
- Measure the operation in question. `tailscale ping` reports a network round trip and whether the route is direct or relayed. Time directory listing and a small disposable file transfer separately to characterize application behavior.
