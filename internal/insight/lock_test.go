package insight

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTryLock_SingleFlightAndRelease(t *testing.T) {
	root := t.TempDir()

	lock, held, err := TryLock(root)
	if err != nil || !held {
		t.Fatalf("first lock: held=%v err=%v", held, err)
	}
	if pid, alive := Holder(root); pid != os.Getpid() || !alive {
		t.Fatalf("holder: pid=%d alive=%v", pid, alive)
	}

	if _, again, err := TryLock(root); err != nil || again {
		t.Fatalf("second lock while held: held=%v err=%v", again, err)
	}

	lock.Release()
	lock.Release() // twice is fine
	if _, alive := Holder(root); alive {
		t.Fatal("released lock still reports a holder")
	}
	if _, held, err := TryLock(root); err != nil || !held {
		t.Fatalf("relock after release: held=%v err=%v", held, err)
	}
}

func TestTryLock_TakesOverStaleLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, LockFileName)
	// A pid that cannot be alive: PIDs are bounded well below this on every
	// platform re_gent runs on.
	if err := os.WriteFile(path, []byte("2147483000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, held, err := TryLock(root)
	if err != nil || !held {
		t.Fatalf("stale takeover: held=%v err=%v", held, err)
	}
	defer lock.Release()
	if pid, _ := Holder(root); pid != os.Getpid() {
		t.Fatalf("lock should now be ours, holder=%d", pid)
	}

	// Garbage content is also stale.
	lock.Release()
	if err := os.WriteFile(path, []byte("not a pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, held, err := TryLock(root); err != nil || !held {
		t.Fatalf("garbage takeover: held=%v err=%v", held, err)
	}
}
