//go:build !darwin

package proc

import "fmt"

func Arguments(pid int) ([]string, map[string]string, error) {
	return nil, nil, fmt.Errorf("process inspection requires macOS")
}
