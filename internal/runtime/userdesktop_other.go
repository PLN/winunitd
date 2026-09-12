//go:build !windows

package runtime

import (
	"fmt"
	"io"
)

func ServeUserDesktopHelper(sid, name string, stdout io.Writer) error {
	return fmt.Errorf("user desktop helper requires Windows SYSTEM")
}

func SetHeadlessUserWorkingDirectory(profile string) error { return nil }
