//go:build !darwin

package main

import "net/http"

func (a *Agent) latency(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
