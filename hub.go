package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Peer struct {
	ID           string   `json:"ID"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	OS           string   `json:"OS"`
	Online       bool     `json:"Online"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

type Remote struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

type HubConfig struct {
	Login      string            `json:"login"`
	LocalID    string            `json:"localID"`
	LocalToken string            `json:"localToken"`
	Remotes    map[string]Remote `json:"remotes"`
	Cloudflare *CloudflareConfig `json:"cloudflare,omitempty"`
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OS        string `json:"os"`
	Address   string `json:"address"`
	Online    bool   `json:"online"`
	Connector bool   `json:"connector"`
	Local     bool   `json:"local"`
}

type Hub struct {
	cfg     HubConfig
	client  *http.Client
	assets  http.Handler
	mu      sync.Mutex
	peers   []Peer
	refresh time.Time
}

func newHub(cfg HubConfig) *Hub {
	return &Hub{cfg: cfg, assets: http.FileServer(http.Dir("/app/web")), client: &http.Client{
		Transport:     &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 8 * time.Second, DisableCompression: true, MaxIdleConns: 24, MaxIdleConnsPerHost: 8},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (h *Hub) request(ctx context.Context, backend, host, token, method, endpoint string, query url.Values, headers http.Header, body io.Reader, contentLength int64) (*http.Response, error) {
	target := url.URL{Scheme: "http", Host: backend, Path: endpoint, RawQuery: query.Encode()}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if host != "" {
		request.Host = host
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for _, field := range []string{"Range", "If-Range", "If-Match", "Content-Type"} {
		if value := headers.Get(field); value != "" {
			request.Header.Set(field, value)
		}
	}
	if body != nil {
		request.ContentLength = contentLength
	}
	return h.client.Do(request)
}

func (h *Hub) inventory(ctx context.Context) ([]Peer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Now().Before(h.refresh) {
		return h.peers, nil
	}
	resp, err := h.request(ctx, "host.docker.internal:18776", "", h.cfg.LocalToken, "GET", "/tailnet", nil, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var peers []Peer
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tailnet unavailable")
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&peers); err != nil {
		return nil, err
	}
	h.peers, h.refresh = peers, time.Now().Add(10*time.Second)
	return peers, nil
}

var tailnetIPv4 = netip.MustParsePrefix("100.64.0.0/10")

func address(p Peer) string {
	for _, raw := range p.TailscaleIPs {
		ip, err := netip.ParseAddr(raw)
		if err == nil && tailnetIPv4.Contains(ip) {
			return ip.String()
		}
	}
	return ""
}

func (h *Hub) devices(w http.ResponseWriter, r *http.Request) {
	peers, err := h.inventory(r.Context())
	if err != nil {
		apiError(w, 503, "暂时无法读取设备列表。")
		return
	}
	output := make([]Device, 0, len(peers))
	for _, p := range peers {
		name := p.HostName
		local := p.ID == h.cfg.LocalID
		remote, paired := h.cfg.Remotes[p.ID]
		switch {
		case local:
			name = "Mac mini"
		case paired:
			name = remote.Name
		case p.OS == "iOS" && (name == "localhost" || name == ""):
			name = strings.Split(p.DNSName, ".")[0]
		}
		output = append(output, Device{p.ID, name, p.OS, address(p), p.Online || local, paired, local})
	}
	sort.SliceStable(output, func(i, j int) bool {
		if output[i].Local != output[j].Local {
			return output[i].Local
		}
		if output[i].Connector != output[j].Connector {
			return output[i].Connector
		}
		return strings.ToLower(output[i].Name) < strings.ToLower(output[j].Name)
	})
	writeJSON(w, output)
}

func (h *Hub) fileRequest(w http.ResponseWriter, r *http.Request, endpoint string) {
	device := r.URL.Query().Get("device")
	backend, host, token := "host.docker.internal:18776", "", h.cfg.LocalToken
	if device != h.cfg.LocalID {
		remote, ok := h.cfg.Remotes[device]
		if !ok {
			apiError(w, 404, "这台设备尚未接入文件服务。")
			return
		}
		peers, err := h.inventory(r.Context())
		if err != nil {
			apiError(w, 503, "暂时无法读取设备列表。")
			return
		}
		ip, dns := "", ""
		for _, p := range peers {
			if p.ID == device && p.Online {
				ip, dns = address(p), strings.TrimSuffix(p.DNSName, ".")
				break
			}
		}
		if ip == "" || dns == "" {
			apiError(w, 503, "设备离线。")
			return
		}
		backend, host, token = net.JoinHostPort(ip, "8776"), net.JoinHostPort(dns, "8776"), remote.Token
	}
	query := url.Values{}
	for _, key := range []string{"location", "path", "offset", "kind"} {
		query.Set(key, r.URL.Query().Get(key))
	}
	var body io.Reader
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		body = r.Body
	}
	resp, err := h.request(r.Context(), backend, host, token, r.Method, endpoint, query, r.Header, body, r.ContentLength)
	if err != nil {
		if endpoint == "/list" || endpoint == "/download" {
			apiError(w, 504, "文件没有响应。如设备上出现文件访问权限提示，请允许后刷新。")
		} else {
			apiError(w, 502, "文件服务未响应。请确认这台设备已运行接入工具。")
		}
		return
	}
	defer resp.Body.Close()
	if device != h.cfg.LocalID && r.Method != http.MethodGet && r.Method != http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
		apiError(w, http.StatusMethodNotAllowed, "此设备的接入工具需要升级。请在该设备的文件页展开「更新设备接入工具」。")
		return
	}
	if resp.StatusCode == 401 || resp.StatusCode == 404 && endpoint == "/storage" || resp.StatusCode >= 500 {
		apiError(w, 502, "文件服务尚未接入或暂时不可用。")
		return
	}
	for _, field := range []string{"Content-Type", "Content-Disposition", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified"} {
		if value := resp.Header.Get(field); value != "" {
			w.Header().Set(field, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *Hub) installer(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device")
	remote, ok := h.cfg.Remotes[deviceID]
	if !ok {
		apiError(w, 404, "这台设备没有接入工具。")
		return
	}
	peers, err := h.inventory(r.Context())
	if err != nil {
		apiError(w, 503, "暂时无法读取设备列表。")
		return
	}
	deviceOS := ""
	for _, p := range peers {
		if p.ID == deviceID {
			deviceOS = p.OS
			break
		}
	}
	if deviceOS == "" {
		apiError(w, 404, "找不到这台设备。")
		return
	}
	if deviceOS == "windows" {
		proxyTarget, proxyHost := "", ""
		for _, p := range peers {
			if p.ID == h.cfg.LocalID {
				proxyTarget = "http://" + net.JoinHostPort(address(p), "8766")
				proxyHost = net.JoinHostPort(strings.TrimSuffix(p.DNSName, "."), "8766")
				break
			}
		}
		h.windowsInstaller(w, remote, proxyTarget, proxyHost)
		return
	}
	architecture := r.URL.Query().Get("arch")
	if architecture != "arm64" && architecture != "amd64" {
		apiError(w, 400, "请选择 Mac 的芯片类型。")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="MacBook-Device-Files.zip"`)
	archive := zip.NewWriter(w)
	defer archive.Close()
	for _, item := range []struct {
		source, name string
		mode         os.FileMode
	}{
		{"Install.command", "Install.command", 0755}, {"device-files-" + architecture, "device-files", 0755},
	} {
		file, err := os.Open(filepath.Join("/app/install", item.source))
		if err != nil {
			return
		}
		header := &zip.FileHeader{Name: "MacBook-Device-Files/" + item.name, Method: zip.Deflate}
		header.SetMode(item.mode)
		writer, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(writer, file)
		}
		file.Close()
		if err != nil {
			return
		}
	}
	header := &zip.FileHeader{Name: "MacBook-Device-Files/agent.json", Method: zip.Deflate}
	header.SetMode(0600)
	if writer, err := archive.CreateHeader(header); err == nil {
		_ = json.NewEncoder(writer).Encode(AgentConfig{Token: remote.Token})
	}
}

func (h *Hub) windowsInstaller(w http.ResponseWriter, remote Remote, proxyTarget, proxyHost string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="Windows-Device-Files.zip"`)
	archive := zip.NewWriter(w)
	defer archive.Close()
	for _, item := range []struct {
		source, name string
		mode         os.FileMode
	}{
		{"Install.bat", "Install.bat", 0644},
		{"Install.ps1", "Install.ps1", 0644},
		{"device-files-windows-amd64.exe", "device-files.exe", 0755},
	} {
		file, err := os.Open(filepath.Join("/app/install", item.source))
		if err != nil {
			return
		}
		header := &zip.FileHeader{Name: "Windows-Device-Files/" + item.name, Method: zip.Deflate}
		header.SetMode(item.mode)
		writer, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(writer, file)
		}
		file.Close()
		if err != nil {
			return
		}
	}
	header := &zip.FileHeader{Name: "Windows-Device-Files/agent.json", Method: zip.Deflate}
	header.SetMode(0600)
	if writer, err := archive.CreateHeader(header); err == nil {
		_ = json.NewEncoder(writer).Encode(AgentConfig{Token: remote.Token, ProxyTarget: proxyTarget, ProxyHost: proxyHost})
	}
}

func (h *Hub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h.serveAuthorized(w, r, equal(r.Header.Get("Tailscale-User-Login"), h.cfg.Login))
}

func (h *Hub) serveAuthorized(w http.ResponseWriter, r *http.Request, authorized bool) {
	protect(w)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !authorized {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(403)
		_, _ = io.WriteString(w, "请通过此网页的登录入口访问。")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		allowed := r.Method == "PUT" && r.URL.Path == "/api/file" ||
			r.Method == "DELETE" && r.URL.Path == "/api/file" ||
			r.Method == "POST" && (r.URL.Path == "/api/upload" || r.URL.Path == "/api/mkdir")
		if !allowed {
			apiError(w, 405, "不支持此操作。")
			return
		}
		if r.Header.Get("X-Device-Files-Action") != "1" {
			apiError(w, 403, "请刷新网页后重新操作。")
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		apiError(w, 403, "请从文件网页中打开。")
		return
	}
	switch r.URL.Path {
	case "/api/devices":
		h.devices(w, r)
	case "/api/storage":
		h.fileRequest(w, r, "/storage")
	case "/api/list":
		h.fileRequest(w, r, "/list")
	case "/api/download":
		h.fileRequest(w, r, "/download")
	case "/api/file":
		h.fileRequest(w, r, "/file")
	case "/api/mkdir":
		h.fileRequest(w, r, "/mkdir")
	case "/api/upload":
		h.fileRequest(w, r, "/upload")
	case "/api/preview":
		h.fileRequest(w, r, "/preview")
	case "/api/installer":
		h.installer(w, r)
	case "/", "/style.css", "/app.js":
		h.assets.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}
