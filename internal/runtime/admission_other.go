//go:build !windows

package runtime

import "fmt"

func UserUnitFilesPresent(*UserToken) (bool, error) {
	return false, fmt.Errorf("user unit admission requires a Windows token")
}
