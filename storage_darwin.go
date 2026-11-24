//go:build darwin

package main

import (
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const openSelectedFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func tailscaleBinary() (string, error) {
	for _, name := range []string{"/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return exec.LookPath("tailscale")
}

func tailscaleStatus(ctx context.Context, home string) ([]byte, error) {
	binary, err := tailscaleBinary()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "/bin/sh", "-c", `"$1" status --json; exit $?`, "device-files", binary)
	command.Env = append(os.Environ(), "HOME="+home)
	return command.Output()
}

func diskInfo(mount string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/diskutil", "info", "-plist", mount).Output()
	if err != nil {
		return nil
	}
	decoder := xml.NewDecoder(strings.NewReader(string(out)))
	values := make(map[string]string)
	key := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			return values
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch element.Name.Local {
		case "key":
			key = ""
			_ = decoder.DecodeElement(&key, &element)
		case "string", "integer":
			var value string
			if decoder.DecodeElement(&value, &element) == nil {
				values[key] = value
			}
		case "true", "false":
			values[key] = element.Name.Local
		}
	}
}

func usage(mount, id, name string) (Disk, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(mount, &stats); err != nil {
		return Disk{}, err
	}
	block := uint64(stats.Bsize)
	return Disk{ID: id, Name: name, Total: stats.Blocks * block, Used: (stats.Blocks - stats.Bfree) * block, Free: stats.Bavail * block}, nil
}

func mountName(raw []int8) string {
	name := make([]byte, 0, len(raw))
	for _, value := range raw {
		if value == 0 {
			break
		}
		name = append(name, byte(value))
	}
	return string(name)
}

func verifyVolume(root *os.Root, location Location) error {
	if location.Volume == "" {
		return nil
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	var volume syscall.Statfs_t
	if err := syscall.Fstatfs(int(file.Fd()), &volume); err != nil {
		return err
	}
	if mountName(volume.Mntonname[:]) != location.Volume || volume.Fsid.Val != location.VolumeID {
		return os.ErrNotExist
	}
	return nil
}

func (a *Agent) storage() Storage {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().Before(a.refresh) {
		return a.cached
	}
	result := Storage{Locations: []Location{}, Disks: []Disk{}}
	seen := make(map[string]bool)
	add := func(location Location, diskName string) {
		if !seen[location.Disk] {
			disk, err := usage(location.Path, location.Disk, diskName)
			if err != nil {
				return
			}
			result.Disks = append(result.Disks, disk)
			seen[location.Disk] = true
		}
		result.Locations = append(result.Locations, location)
	}
	mounts := []string{"/System/Volumes/Data"}
	volumes, _ := os.ReadDir("/Volumes")
	for _, volume := range volumes {
		if volume.IsDir() && !strings.HasPrefix(volume.Name(), ".") {
			mounts = append(mounts, filepath.Join("/Volumes", volume.Name()))
		}
	}
	infos := make([]map[string]string, len(mounts))
	var probes sync.WaitGroup
	for index, mount := range mounts {
		probes.Go(func() { infos[index] = diskInfo(mount) })
	}
	probes.Wait()
	homeDisk := infos[0]["APFSContainerReference"]
	if homeDisk == "" {
		homeDisk = "home"
	}
	add(Location{ID: "home", Name: "个人文件", Disk: homeDisk, Path: a.cfg.Home}, "内置磁盘")
	for index, mount := range mounts[1:] {
		info := infos[index+1]
		if info["MountPoint"] != mount || info["Internal"] != "false" || info["VirtualOrPhysical"] == "Virtual" {
			continue
		}
		if info["BusProtocol"] == "Disk Image" || info["DeviceProtocol"] == "Disk Image" {
			continue
		}
		uuid := info["VolumeUUID"]
		key := uuid
		if key == "" {
			key = mount
		}
		digest := sha256.Sum256([]byte(key))
		id := fmt.Sprintf("disk-%x", digest[:6])
		pool := info["APFSContainerReference"]
		if pool == "" {
			pool = id
		}
		var stats syscall.Statfs_t
		if syscall.Statfs(mount, &stats) != nil {
			continue
		}
		location := Location{ID: id, Name: filepath.Base(mount), Disk: pool, Path: mount, Volume: mount, VolumeID: stats.Fsid.Val}
		add(location, location.Name)
		if workspace := a.cfg.Workspace; workspace != nil && workspace.Mount == mount && uuid != "" && workspace.UUID == uuid {
			path := filepath.Join(mount, workspace.Path)
			if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved == path && strings.HasPrefix(path, mount+"/") {
				location.ID, location.Name, location.Path = "workspace", "共享空间", path
				add(location, filepath.Base(mount))
			}
		}
	}
	a.cached, a.refresh = result, time.Now().Add(20*time.Second)
	return result
}
