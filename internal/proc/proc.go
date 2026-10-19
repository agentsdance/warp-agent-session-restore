// Package proc inspects running processes to find live agent CLIs, the Warp
// pane each one belongs to, and the session files they hold open.
package proc

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Process is one entry from the process table.
type Process struct {
	PID     int
	PPID    int
	Command string   // full command line as reported by ps
	Argv    []string // Command split on spaces
	Env     map[string]string
}

// List returns every process visible to the current user.
func List() ([]Process, error) {
	out, err := exec.Command("ps", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil, err
	}

	var procs []Process
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// "<pid> <ppid> <command...>"
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, fields[0]), " "))
		cmd = strings.TrimSpace(strings.TrimPrefix(cmd, fields[1]))
		procs = append(procs, Process{PID: pid, PPID: ppid, Command: cmd})
	}
	return procs, sc.Err()
}

var paneUUIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func NormalizePane(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if !paneUUIDRe.MatchString(s) {
		return ""
	}
	return s
}

// WarpID identifies a particular app lifetime, including across PID reuse.
func WarpID(procs []Process) string {
	for _, p := range procs {
		if !strings.Contains(p.Command, "/Warp.app/Contents/MacOS/") {
			continue
		}
		parentIsWarp := false
		for _, q := range procs {
			if q.PID == p.PPID && q.Command == p.Command {
				parentIsWarp = true
			}
		}
		if parentIsWarp {
			continue
		}
		start := StartTime(p.PID)
		if !start.IsZero() {
			return fmtID(p.PID, start)
		}
	}
	return ""
}

func fmtID(pid int, start time.Time) string {
	return strconv.Itoa(pid) + "-" + strconv.FormatInt(start.Unix(), 10)
}

func Hydrate(p *Process) error {
	args, env, err := Arguments(p.PID)
	if err != nil {
		return err
	}
	p.Argv, p.Env = args, env
	return nil
}

func Basename(s string) string { return filepath.Base(s) }

// PaneUUID reads the Warp pane UUID from a process environment. Warp exports it
// into every shell it starts, so any agent launched from a pane inherits it.
// Returns an empty string when the process was not started by Warp.
func PaneUUID(pid int) string {
	_, env, err := Arguments(pid)
	if err != nil {
		return ""
	}
	return NormalizePane(env["WARP_TERMINAL_SESSION_UUID"])
}

// StartTime returns when a process started.
func StartTime(pid int) time.Time {
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}
	}
	s := strings.TrimSpace(string(out))
	// ps prints e.g. "Thu Aug 13 10:05:41 2026" in the local timezone.
	t, err := time.ParseInLocation("Mon Jan  2 15:04:05 2006", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// OpenFiles maps each pid to the regular files it currently has open.
func OpenFiles(pids []int) map[int][]string {
	res := map[int][]string{}
	if len(pids) == 0 {
		return res
	}
	strs := make([]string, len(pids))
	for i, p := range pids {
		strs[i] = strconv.Itoa(p)
	}
	// -Fpn gives machine readable output: "p<pid>" then "n<name>" lines.
	// lsof exits non-zero when some pids are gone, so the output is used regardless.
	out, _ := exec.Command("lsof", "-p", strings.Join(strs, ","), "-Fpn").Output()

	cur := 0
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			if v, err := strconv.Atoi(line[1:]); err == nil {
				cur = v
			}
		case 'n':
			if cur != 0 {
				res[cur] = append(res[cur], line[1:])
			}
		}
	}
	return res
}

// CWD returns the working directory of a process.
func CWD(pid int) string {
	out, _ := exec.Command("lsof", "-p", strconv.Itoa(pid), "-a", "-d", "cwd", "-Fn").Output()
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
}

// Descendants returns pid and every process below it in the tree.
func Descendants(procs []Process, pid int) []int {
	children := map[int][]int{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	var out []int
	var walk func(int)
	walk = func(cur int) {
		out = append(out, cur)
		for _, c := range children[cur] {
			walk(c)
		}
	}
	walk(pid)
	return out
}
