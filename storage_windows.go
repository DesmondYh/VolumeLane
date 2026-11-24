//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const openSelectedFlags = 0

func tailscaleBinary() (string, error) {
	for _, name := range []string{`C:\Program Files\Tailscale\tailscale.exe`, `C:\Program Files (x86)\Tailscale\tailscale.exe`} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return exec.LookPath("tailscale.exe")
}

func tailscaleStatus(ctx context.Context, _ string) ([]byte, error) {
	binary, err := tailscaleBinary()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, binary, "status", "--json").Output()
}

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var procGetLogicalDrives = kernel32.NewProc("GetLogicalDrives")
var procGetDriveType = kernel32.NewProc("GetDriveTypeW")
var procGetDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
var procGetVolumeInformation = kernel32.NewProc("GetVolumeInformationW")

type winDrive struct {
	root  string
	label string
	total uint64
	free  uint64
}

func windowsDrives() map[string]winDrive {
	mask, _, _ := procGetLogicalDrives.Call()
	result := make(map[string]winDrive)
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		rootPtr := uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root)))
		kind, _, _ := procGetDriveType.Call(rootPtr)
		if kind != 3 {
			continue
		}
		var freeAvailable, total, totalFree uint64
		ok, _, _ := procGetDiskFreeSpaceEx.Call(rootPtr,
			uintptr(unsafe.Pointer(&freeAvailable)),
			uintptr(unsafe.Pointer(&total)),
			uintptr(unsafe.Pointer(&totalFree)))
		if ok == 0 || total == 0 {
			continue
		}
		label := "本地磁盘"
		var name [260]uint16
		var serial, maxLen, flags uint32
		procGetVolumeInformation.Call(rootPtr,
			uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)),
			uintptr(unsafe.Pointer(&serial)),
			uintptr(unsafe.Pointer(&maxLen)),
			uintptr(unsafe.Pointer(&flags)))
		if text := syscall.UTF16ToString(name[:]); text != "" {
			label = text
		}
		result[letter] = winDrive{root: root, label: label, total: total, free: freeAvailable}
	}
	return result
}

func (a *Agent) storage() Storage {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().Before(a.refresh) {
		return a.cached
	}
	result := Storage{Locations: []Location{}, Disks: []Disk{}}
	drives := windowsDrives()
	letters := make([]string, 0, len(drives))
	for letter := range drives {
		letters = append(letters, letter)
	}
	sort.Strings(letters)
	seen := make(map[string]bool)
	addDisk := func(letter string, drive winDrive) string {
		id := "drive-" + letter
		if !seen[id] {
			result.Disks = append(result.Disks, Disk{
				ID: id, Name: fmt.Sprintf("%s (%s:)", drive.label, letter),
				Total: drive.total, Used: drive.total - drive.free, Free: drive.free,
			})
			seen[id] = true
		}
		return id
	}
	homeLetter := strings.ToUpper(strings.TrimSuffix(filepath.VolumeName(a.cfg.Home), ":"))
	if drive, ok := drives[homeLetter]; ok {
		diskID := addDisk(homeLetter, drive)
		result.Locations = append(result.Locations, Location{ID: "home", Name: "个人文件", Disk: diskID, Path: a.cfg.Home})
	}
	for _, letter := range letters {
		if letter == homeLetter {
			continue
		}
		drive := drives[letter]
		diskID := addDisk(letter, drive)
		result.Locations = append(result.Locations, Location{
			ID: diskID, Name: fmt.Sprintf("%s (%s:)", drive.label, letter), Disk: diskID, Path: drive.root,
		})
	}
	a.cached, a.refresh = result, time.Now().Add(20*time.Second)
	return result
}
func verifyVolume(_ *os.Root, _ Location) error { return nil }
