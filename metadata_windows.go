//go:build windows

package main

import "os"

func copyFileMetadata(_ *os.File, destination *os.File, info os.FileInfo) error {
	return destination.Chmod(info.Mode().Perm())
}