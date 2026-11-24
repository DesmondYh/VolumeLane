//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxLatencyProbes = 8

func (a *Agent) latency(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ListTailnet {
		http.NotFound(w, r)
		return
	}
	ids := r.URL.Query()["peer"]
	if len(ids) == 0 || len(ids) > maxLatencyCandidates {
		writeJSON(w, latencyResponse{Samples: []latencySample{}})
		return
	}
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	cache := &a.latencies
	cache.mu.Lock()
	refresh := !time.Now().Before(cache.nextAttempt)
	samples := cache.samples
	if refresh {
		cache.nextAttempt = time.Now().Add(30 * time.Second)
	}
	cache.mu.Unlock()
	if refresh {
		samples = a.measureLatency(r.Context(), ids)
		cache.mu.Lock()
		cache.samples = samples
		cache.mu.Unlock()
	}
	result := latencyResponse{Samples: []latencySample{}}
	for _, sample := range samples {
		if requested[sample.ID] {
			result.Samples = append(result.Samples, sample)
		}
	}
	writeJSON(w, result)
}

func activeLANSubnets() []lanSubnet {
	interfaces, _ := net.Interfaces()
	var subnets []lanSubnet
	for _, iface := range interfaces {
		// On macOS, enN interfaces are physical network adapters. VPN and
		// virtual interfaces can have overlapping private addresses.
		if !strings.HasPrefix(iface.Name, "en") || iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		if _, err := strconv.Atoi(iface.Name[2:]); err != nil {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil {
				subnets = append(subnets, lanSubnet{iface.Name, prefix})
			}
		}
	}
	return subnets
}

func (a *Agent) measureLatency(parent context.Context, ids []string) []latencySample {
	// Tailscale adds a delay after relay probes. This bounds all subprocesses,
	// including inventory, while allowing one two-second probe per peer.
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	binary, err := tailscaleBinary()
	if err != nil {
		return nil
	}
	status, err := tailscaleStatus(ctx, a.cfg.Home)
	if err != nil {
		return nil
	}
	var report struct{ Peer map[string]Peer }
	if json.Unmarshal(status, &report) != nil {
		return nil
	}
	eligible := make(map[string]string)
	for _, peer := range report.Peer {
		if peer.Online && (peer.OS == "macOS" || peer.OS == "windows") {
			eligible[peer.ID] = address(peer)
		}
	}
	lan := activeLANSubnets()
	samples := make([]latencySample, maxLatencyProbes)
	var pending sync.WaitGroup
	used := 0
	for _, id := range ids {
		target := eligible[id]
		if target == "" {
			continue
		}
		delete(eligible, id)
		index := used
		pending.Go(func() {
			command := exec.CommandContext(ctx, binary, "ping", "--c=1", "--until-direct=false", "--timeout=2s", target)
			command.Env = append(os.Environ(), "HOME="+a.cfg.Home)
			output, _ := command.Output()
			if sample, ok := parseTailscaleLatency(string(output), target, lan); ok {
				sample.ID = id
				samples[index] = sample
			}
		})
		used++
		if used == maxLatencyProbes {
			break
		}
	}
	pending.Wait()
	result := make([]latencySample, 0, used)
	for _, sample := range samples[:used] {
		if sample.ID != "" {
			result = append(result, sample)
		}
	}
	return result
}
