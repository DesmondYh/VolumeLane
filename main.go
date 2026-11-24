package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func protect(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func readConfig(path string, config any) {
	file, err := os.Open(path)
	if err != nil {
		log.Fatal("cannot open service configuration")
	}
	defer file.Close()
	if json.NewDecoder(file).Decode(config) != nil {
		log.Fatal("invalid service configuration")
	}
}

func server(address string, handler http.HandlerFunc) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 45 * time.Second, MaxHeaderBytes: 24 << 10}
}

func main() {
	if len(os.Args) != 4 {
		log.Fatal("usage: device-files agent|hub config listen-address")
	}
	var handler http.HandlerFunc
	switch os.Args[1] {
	case "agent":
		var cfg AgentConfig
		readConfig(os.Args[2], &cfg)
		if cfg.Home == "" {
			var err error
			cfg.Home, err = os.UserHomeDir()
			if err != nil {
				log.Fatal("cannot locate the home directory")
			}
		}
		if len(cfg.Token) < 32 {
			log.Fatal("missing agent credentials")
		}
		handler = (&Agent{cfg: cfg}).serveHTTP
		if cfg.ProxyTarget != "" && cfg.ProxyHost != "" {
			go startLocalProxy(cfg.ProxyTarget, cfg.ProxyHost, cfg.ProxyListen)
		}
	case "hub":
		var cfg HubConfig
		readConfig(os.Args[2], &cfg)
		if cfg.Login == "" || cfg.LocalID == "" || len(cfg.LocalToken) < 32 {
			log.Fatal("missing hub configuration")
		}
		hub := newHub(cfg)
		handler = hub.serveHTTP
		if cfg.Cloudflare != nil {
			access, err := newCloudflareAccess(*cfg.Cloudflare)
			if err != nil {
				log.Fatal(err)
			}
			go func() {
				log.Fatal(server("0.0.0.0:8081", func(w http.ResponseWriter, r *http.Request) {
					hub.serveAuthorized(w, r, access.authorized(r))
				}).ListenAndServe())
			}()
		}
	default:
		log.Fatal("unknown service mode")
	}
	fmt.Println("Device Files listening on", os.Args[3])
	log.Fatal(server(os.Args[3], handler).ListenAndServe())
}
