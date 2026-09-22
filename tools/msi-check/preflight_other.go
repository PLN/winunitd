//go:build !windows

package main

import "os"

func pathIsReparse(path string, info os.FileInfo) (bool, error) {
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return true, nil
	}
	return false, nil
}
