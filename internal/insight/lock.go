package insight

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LockFileName is the single-flight lock the worker holds under .regent/.
const LockFileName = "insight.lock"

// Lock is a held worker lock. Release removes it.
type Lock struct {
	path string
}

// TryLock takes the worker lock for the store at root. The second value is
// false, with no error, when another live worker holds it.
//
// The lock is a file created with O_EXCL holding the owner's pid, the same
// shape store.refs uses for ref updates. A lock whose pid is no longer alive
// is stale (the worker died without releasing) and is taken over.
func TryLock(root string) (*Lock, bool, error) {
	path := filepath.Join(root, LockFileName)
	for attempt := range 2 {
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0o644)
		if err == nil {
			_, werr := syscall.Write(fd, []byte(strconv.Itoa(os.Getpid())+"\n"))
			_ = syscall.Close(fd)
			if werr != nil {
				_ = os.Remove(path)
				return nil, false, fmt.Errorf("write %s: %w", path, werr)
			}
			return &Lock{path: path}, true, nil
		}
		if !errors.Is(err, syscall.EEXIST) {
			return nil, false, fmt.Errorf("create %s: %w", path, err)
		}
		pid, alive := lockHolder(path)
		if alive {
			return nil, false, nil
		}
		// Stale: the recorded process is gone. Remove and retry once; a
		// second EEXIST means someone else won the race, which is fine.
		if pid != 0 || attempt == 0 {
			_ = os.Remove(path)
			continue
		}
		return nil, false, nil
	}
	return nil, false, nil
}

// Holder reports the pid recorded in the lock at root and whether that
// process is alive. A missing lock is (0, false).
func Holder(root string) (int, bool) {
	return lockHolder(filepath.Join(root, LockFileName))
}

// Release removes the lock. Calling it twice is harmless.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = os.Remove(l.path)
}

func lockHolder(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, processAlive(pid)
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence and permission without delivering anything.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
