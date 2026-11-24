//go:build !darwin && !windows

package main

import (
	"os"
	"syscall"
)

func copyFileMetadata(_ *os.File, destination *os.File, info os.FileInfo) error {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := destination.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
	}
	return destination.Chmod(info.Mode().Perm())
}
