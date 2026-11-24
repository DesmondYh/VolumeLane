//go:build !darwin && !windows

package main

import (
	"context"
	"errors"
	"os"
)

const openSelectedFlags = 0

func tailscaleStatus(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("tailnet listing is not supported on this platform")
}

func (a *Agent) storage() Storage {
	return Storage{Locations: []Location{}, Disks: []Disk{}}
}
func verifyVolume(_ *os.Root, _ Location) error { return nil }
