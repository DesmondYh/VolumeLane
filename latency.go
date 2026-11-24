package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type latencySample struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Scope  string `json:"scope"`
	Method string `json:"method"`
	MS     int64  `json:"ms"`
}

type latencyResponse struct {
	Samples []latencySample `json:"samples"`
}

type latencyCache struct {
	mu          sync.Mutex
	nextAttempt time.Time
	samples     []latencySample
}

const maxLatencyCandidates = 64

func (h *Hub) latency(w http.ResponseWriter, r *http.Request) {
	result := latencyResponse{Samples: []latencySample{}}
	query := url.Values{}
	ids := make([]string, 0, len(h.cfg.Remotes))
	for id := range h.cfg.Remotes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids[:min(len(ids), maxLatencyCandidates)] {
		query.Add("peer", id)
	}
	if len(query) == 0 {
		writeJSON(w, result)
		return
	}
	response, err := h.request(r.Context(), "host.docker.internal:18776", "", h.cfg.LocalToken, http.MethodGet, "/latency", query, nil, nil, 0)
	if err == nil {
		defer response.Body.Close()
		var measured latencyResponse
		if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&measured) == nil {
			for _, sample := range measured.Samples {
				if remote, ok := h.cfg.Remotes[sample.ID]; ok {
					sample.Name = remote.Name
					result.Samples = append(result.Samples, sample)
				}
			}
		}
	}
	writeJSON(w, result)
}

type lanSubnet struct {
	iface  string
	prefix netip.Prefix
}

var tailnetIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

func parseTailscaleLatency(output, target string, lan []lanSubnet) (latencySample, bool) {
	for line := range strings.SplitSeq(output, "\n") {
		peer, value, ok := strings.Cut(strings.TrimSpace(line), ") via ")
		if !ok || !strings.HasPrefix(peer, "pong from ") || !strings.HasSuffix(peer, "("+target) {
			continue
		}
		route, rawDuration, ok := strings.Cut(value, " in ")
		duration, err := time.ParseDuration(rawDuration)
		if !ok || err != nil || duration < 0 || duration > 2*time.Second {
			continue
		}
		sample := latencySample{Scope: "wan", Method: "relay", MS: duration.Milliseconds()}
		if strings.HasPrefix(route, "DERP(") || strings.HasPrefix(route, "peer-relay(") {
			return sample, true
		}
		endpoint, err := netip.ParseAddrPort(route)
		if err != nil || endpoint.Port() == 0 {
			continue
		}
		sample.Method = "direct"
		ip := endpoint.Addr().Unmap()
		if !tailnetIPv4.Contains(ip) && !tailnetIPv6.Contains(ip) && !ip.IsLoopback() && !ip.IsUnspecified() {
			for _, subnet := range lan {
				if (ip.Zone() == "" || ip.Zone() == subnet.iface) && subnet.prefix.Contains(ip.WithZone("")) {
					sample.Scope = "lan"
					break
				}
			}
		}
		return sample, true
	}
	return latencySample{}, false
}
