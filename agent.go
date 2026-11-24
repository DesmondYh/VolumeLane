package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Location struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Disk     string   `json:"disk"`
	Path     string   `json:"-"`
	Volume   string   `json:"-"`
	VolumeID [2]int32 `json:"-"`
}

type Disk struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

type Storage struct {
	Locations []Location `json:"locations"`
	Disks     []Disk     `json:"disks"`
}

type Entry struct {
	Name      string    `json:"name"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
}

type Agent struct {
	cfg     AgentConfig
	mu      sync.Mutex
	writeMu sync.Mutex
	cached  Storage
	refresh time.Time
}

type AgentConfig struct {
	Token       string           `json:"token"`
	Home        string           `json:"home"`
	ListTailnet bool             `json:"listTailnet"`
	ProxyTarget string           `json:"proxyTarget,omitempty"`
	ProxyHost   string           `json:"proxyHost,omitempty"`
	ProxyListen string `json:"proxyListen,omitempty"`
	Workspace   *WorkspaceConfig `json:"workspace,omitempty"`
}

type WorkspaceConfig struct {
	Mount string `json:"mount"`
	UUID  string `json:"uuid"`
	Path  string `json:"path"`
}

func (a *Agent) location(id string) (Location, error) {
	if id == "home" {
		return Location{ID: id, Path: a.cfg.Home}, nil
	}
	for _, location := range a.storage().Locations {
		if id == location.ID {
			return location, nil
		}
	}
	return Location{}, os.ErrNotExist
}

func (location Location) openRoot() (*os.Root, error) {
	root, err := os.OpenRoot(location.Path)
	if err != nil {
		return nil, err
	}
	if err := verifyVolume(root, location); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func validPath(location, value string) bool {
	if value == "" {
		return true
	}
	if !path.IsAbs(value) && path.Clean(value) == value && !strings.ContainsAny(value, "\\:\x00") {
		for index, part := range strings.Split(value, "/") {
			blockedSystem := location == "home" && index == 0 && (strings.EqualFold(part, "Library") || strings.EqualFold(part, "AppData") || strings.HasPrefix(strings.ToUpper(part), "NTUSER"))
			if strings.HasPrefix(part, ".") || blockedSystem {
				return false
			}
		}
		return true
	}
	return false
}

func (a *Agent) open(w http.ResponseWriter, r *http.Request) (*os.File, string) {
	id, relative := r.URL.Query().Get("location"), r.URL.Query().Get("path")
	if !validPath(id, relative) {
		apiError(w, http.StatusForbidden, "此路径不在可浏览范围内。")
		return nil, relative
	}
	root, leaf, err := a.fileParent(id, relative)
	if err == nil {
		defer root.Close()
		file, openErr := openSelected(root, leaf, os.O_RDONLY)
		if openErr == nil {
			return file, relative
		}
		err = openErr
	}
	fileError(w, err)
	return nil, relative
}

func (a *Agent) list(w http.ResponseWriter, r *http.Request) {
	file, relative := a.open(w, r)
	if file == nil {
		return
	}
	defer file.Close()
	children, err := file.ReadDir(-1)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			apiError(w, 403, "系统尚未允许读取此目录。")
		} else {
			apiError(w, 400, "无法读取这个文件夹。")
		}
		return
	}
	id := r.URL.Query().Get("location")
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		if child.Type()&os.ModeSymlink != 0 || !validPath(id, path.Join(relative, child.Name())) {
			continue
		}
		if !child.Type().IsRegular() && !child.IsDir() {
			continue
		}
		info, err := child.Info()
		if err == nil {
			entries = append(entries, Entry{child.Name(), child.IsDir(), info.Size(), info.ModTime()})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	start, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if start < 0 || start > len(entries) {
		start = 0
	}
	end := min(start+250, len(entries))
	writeJSON(w, map[string]any{"entries": entries[start:end], "count": len(entries), "next": end})
}

func (a *Agent) download(w http.ResponseWriter, r *http.Request) {
	file, relative := a.open(w, r)
	if file == nil {
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		apiError(w, http.StatusBadRequest, "请打开一个普通文件。")
		return
	}
	name := path.Base(relative)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (a *Agent) tailnet(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ListTailnet {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	out, err := tailscaleStatus(ctx, a.cfg.Home)
	if err != nil {
		apiError(w, 503, "无法读取 Tailscale 设备。")
		return
	}
	var report struct {
		Self Peer
		Peer map[string]Peer
	}
	if err := json.Unmarshal(out, &report); err != nil {
		log.Printf("could not decode tailnet status: %T; syntax error", err)
		apiError(w, 503, "无法读取 Tailscale 设备。")
		return
	}
	if report.Self.ID == "" {
		apiError(w, 503, "Tailscale 尚未连接。")
		return
	}
	peers := []Peer{report.Self}
	for _, peer := range report.Peer {
		peers = append(peers, peer)
	}
	writeJSON(w, peers)
}

func (a *Agent) serveHTTP(w http.ResponseWriter, r *http.Request) {
	protect(w)
	if !equal(r.Header.Get("Authorization"), "Bearer "+a.cfg.Token) {
		apiError(w, 401, "连接凭据无效。")
		return
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch r.URL.Path {
	case "/storage":
		if !read {
			apiError(w, 405, "不支持此操作。")
			return
		}
		writeJSON(w, a.storage())
	case "/list":
		if !read {
			apiError(w, 405, "不支持此操作。")
			return
		}
		a.list(w, r)
	case "/download":
		if !read {
			apiError(w, 405, "不支持此操作。")
			return
		}
		a.download(w, r)
	case "/tailnet":
		if !read {
			apiError(w, 405, "不支持此操作。")
			return
		}
		a.tailnet(w, r)
	case "/file":
		if r.Method == http.MethodPut {
			a.saveFile(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			a.deleteFile(w, r)
			return
		}
		if read {
			a.readFileDetails(w, r)
			return
		}
		apiError(w, 405, "不支持此操作。")
	case "/mkdir":
		if r.Method != http.MethodPost {
			apiError(w, 405, "不支持此操作。")
			return
		}
		a.makeFolder(w, r)
	case "/upload":
		if r.Method == http.MethodPost {
			a.uploadFile(w, r)
			return
		}
		apiError(w, 405, "不支持此操作。")
	case "/preview":
		if !read {
			apiError(w, 405, "不支持此操作。")
			return
		}
		a.previewImage(w, r)
	default:
		http.NotFound(w, r)
	}
}
