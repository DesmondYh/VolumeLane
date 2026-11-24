package main

import (
	"errors"
	"os"
	"sync"

	"github.com/ebitengine/purego"
)

var metadataCopier = sync.OnceValues(func() (func(int32, int32, uintptr, uint32) int32, error) {
	library, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	symbol, err := purego.Dlsym(library, "fcopyfile")
	if err != nil {
		return nil, err
	}
	var copyfile func(int32, int32, uintptr, uint32) int32
	purego.RegisterFunc(&copyfile, symbol)
	return copyfile, nil
})

func copyFileMetadata(source, destination *os.File, _ os.FileInfo) error {
	copyfile, err := metadataCopier()
	if err != nil {
		return err
	}
	const copyfileMetadata = 7 // COPYFILE_ACL | COPYFILE_STAT | COPYFILE_XATTR
	if copyfile(int32(source.Fd()), int32(destination.Fd()), 0, copyfileMetadata) != 0 {
		return errors.New("fcopyfile could not preserve file metadata")
	}
	return nil
}
