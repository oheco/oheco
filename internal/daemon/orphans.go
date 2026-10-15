package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HarmonyOS may return EINVAL from /proc/PID/stat during task exit. Retry it
// rather than assuming the task is gone and releasing its service lock early.
func readProcessStat(path string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		data, err := os.ReadFile(path)
		if !errors.Is(err, syscall.EINVAL) || attempt == 5 {
			return data, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func enableSubreaper() error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("enable child subreaper: %w", errno)
	}
	return nil
}

// If a runner itself dies, its descendants are adopted by this daemon. Inspect
// current kernel parent/session ownership, never a persisted PID, before cleanup.
// Runners use separate sessions, so other runners' Wait calls cannot conflict.
func cleanupSession(session int, grace time.Duration) error {
	started := time.Now()
	signaled := map[int]bool{}
	for {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return err
		}
		found := false
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 {
				continue
			}
			data, err := readProcessStat(filepath.Join("/proc", entry.Name(), "stat"))
			if err != nil {
				if errors.Is(err, syscall.EINVAL) {
					return fmt.Errorf("cannot verify process cleanup: %w", err)
				}
				continue
			}
			end := strings.LastIndexByte(string(data), ')')
			if end < 0 {
				continue
			}
			fields := strings.Fields(string(data[end+1:]))
			if len(fields) < 4 {
				continue
			}
			parent, _ := strconv.Atoi(fields[1])
			group, _ := strconv.Atoi(fields[2])
			sid, _ := strconv.Atoi(fields[3])
			if parent != os.Getpid() || sid != session || group <= 0 {
				continue
			}
			// Reap only an adopted child, not any exec.Cmd-managed runner.
			if reaped, _ := syscall.Wait4(pid, nil, syscall.WNOHANG, nil); reaped == pid {
				continue
			}
			found = true
			if time.Since(started) >= grace {
				_ = syscall.Kill(-group, syscall.SIGKILL)
			} else if !signaled[group] {
				_ = syscall.Kill(-group, syscall.SIGTERM)
				signaled[group] = true
			}
		}
		if !found {
			return nil
		}
		if time.Since(started) > grace+3*time.Second {
			return fmt.Errorf("runner session %d still has descendants; refusing a duplicate start", session)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
