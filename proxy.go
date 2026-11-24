package main

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func startLocalProxy(target, host, listen string) {
	if listen == "" {
		listen = "127.0.0.1:18766"
	}
	address, _, err := net.SplitHostPort(listen)
	if err != nil || address != "127.0.0.1" {
		log.Print("local proxy requires a loopback address")
		return
	}
	u, err := url.Parse(target)
	if err != nil {
		log.Printf("local proxy target invalid: %v", err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = host
	}
	proxy.FlushInterval = 200 * time.Millisecond
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		apiError(w, http.StatusBadGateway, "无法连接 Mac mini 网页。请确认 Tailscale 已连接。")
	}
	serve := func(w http.ResponseWriter, r *http.Request) {
		if r.Host != listen {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(w, r)
	}
	if err := server(listen, serve).ListenAndServe(); err != nil {
		log.Printf("local proxy unavailable: %v", err)
	}
}
