//go:build !windows

package manager

import "os"

func admissionFileTrusted(*os.File) error { return nil }
