package proc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// Arguments reads the original NUL-delimited argv, not ps's lossy rendering.
// Only the two needed environment values are retained; secrets are discarded.
func Arguments(pid int) ([]string, map[string]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil, err
	}
	return parseProcArgs(b)
}

func parseProcArgs(b []byte) ([]string, map[string]string, error) {
	if len(b) < 4 {
		return nil, nil, fmt.Errorf("short process arguments")
	}
	n := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 || n < 1 || n > 65536 {
		return nil, nil, fmt.Errorf("invalid process arguments")
	}
	b = bytes.TrimLeft(b[i+1:], "\x00")
	var args []string
	for j := 0; j < n; j++ {
		i = bytes.IndexByte(b, 0)
		if i < 0 {
			return nil, nil, fmt.Errorf("truncated process arguments")
		}
		args = append(args, string(b[:i]))
		b = b[i+1:]
	}
	env := map[string]string{}
	for _, field := range bytes.Split(b, []byte{0}) {
		k, v, ok := strings.Cut(string(field), "=")
		if ok && (k == "WARP_TERMINAL_SESSION_UUID" || k == "CODEX_HOME" || k == "CLAUDE_CONFIG_DIR") {
			env[k] = v
		}
	}
	return args, env, nil
}
