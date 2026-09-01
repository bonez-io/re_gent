package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/bonez-io/re_gent/internal/store"
)

var storeOpener = struct {
	sync.RWMutex
	open func(string) (*store.Store, error)
}{open: store.OpenFromDir}

var notPulledReporter = struct {
	sync.RWMutex
	report func(io.Writer) bool
}{report: func(io.Writer) bool { return false }}

// SetStoreOpener installs the store selection made by the application command
// edge. It returns a restore function for embedders and focused tests.
func SetStoreOpener(open func(string) (*store.Store, error)) func() {
	storeOpener.Lock()
	previous := storeOpener.open
	storeOpener.open = open
	storeOpener.Unlock()
	return func() {
		storeOpener.Lock()
		storeOpener.open = previous
		storeOpener.Unlock()
	}
}

// SetNotPulledReporter installs command-edge presentation for an empty remote
// cache. The reader only knows a generic state marker, never remote config.
func SetNotPulledReporter(report func(io.Writer) bool) {
	notPulledReporter.Lock()
	notPulledReporter.report = report
	notPulledReporter.Unlock()
}

type NotPulledError struct{ Message string }

func (e *NotPulledError) Error() string { return e.Message }

// openStoreFromCWD delegates to the storage policy selected at the command
// edge. Read and workspace commands do not inspect remote configuration.
func openStoreFromCWD() (*store.Store, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return openStoreForCWD(cwd)
}

// reportNotPulled formats the generic marker returned by the command edge.
func reportNotPulled(w io.Writer, err error) bool {
	var notPulled *NotPulledError
	if !errors.As(err, &notPulled) {
		return false
	}
	notPulledReporter.RLock()
	report := notPulledReporter.report
	notPulledReporter.RUnlock()
	if !report(w) {
		fmt.Fprintln(w, notPulled.Error())
	}
	return true
}

func reportEmptyServerModeCache(w io.Writer) bool {
	notPulledReporter.RLock()
	report := notPulledReporter.report
	notPulledReporter.RUnlock()
	return report(w)
}

// openStoreForCWD is the explicit-directory form used by tests and commands
// that already know their workspace.
func openStoreForCWD(cwd string) (*store.Store, error) {
	storeOpener.RLock()
	open := storeOpener.open
	storeOpener.RUnlock()
	return open(cwd)
}
