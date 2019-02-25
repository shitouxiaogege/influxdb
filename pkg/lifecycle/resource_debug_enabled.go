// +build debug_ref

package lifecycle

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
)

// When in debug mode, we associate each reference an id and with that id the
// stack trace that created it. We can't directly refer to the reference here
// because we also associate a finalizer to print to stderr if a reference is
// leaked, including where it came from if possible.

func resourceClosed() error {
	var buf [4096]byte
	return fmt.Errorf("resource closed:\n%s", buf[:runtime.Stack(buf[:], false)])
}

// liveReferences keeps track of the stack traces of all of the live references.
type liveReferences struct {
	mu   sync.Mutex
	id   uint64
	live map[uint64][]uintptr
}

var live = &liveReferences{
	live: make(map[uint64][]uintptr),
}

// finishId informs the liveReferences that the id is no longer in use.
func (l *liveReferences) untrack(r *Reference) {
	l.mu.Lock()
	if _, ok := l.live[r.id]; ok {
		delete(l.live, r.id)
		runtime.SetFinalizer(r, nil)
	}
	l.mu.Unlock()
}

// withFinalizer associates a finalizer with the Reference that will cause it
// to print a leak message if it is not closed before it is garbage collected.
func (l *liveReferences) track(r *Reference) *Reference {
	var buf [32]uintptr
	pcs := append([]uintptr(nil), buf[:runtime.Callers(3, buf[:])]...)

	l.mu.Lock()
	r.id, l.id = l.id, l.id+1
	l.live[r.id] = pcs
	l.mu.Unlock()

	runtime.SetFinalizer(r, func(r *Reference) {
		l.leaked(r)
		r.Release()
	})

	return r
}

// leaked prints a loud message on stderr that the Reference was leaked and
// what was responsible for calling it.
func (l *liveReferences) leaked(r *Reference) {
	l.mu.Lock()
	pcs, ok := l.live[r.id]
	l.mu.Unlock()

	if !ok {
		fmt.Fprintln(os.Stderr, "=====================================================")
		fmt.Fprintln(os.Stderr, "=== Leaked a reference with no stack associated!? ===")
		fmt.Fprintln(os.Stderr, "=====================================================")
		return
	}

	fmt.Fprintln(os.Stderr, "=====================================================")
	fmt.Fprintln(os.Stderr, "=== Leaked a reference! Created from")
	summarizeStack(os.Stderr, pcs)
	fmt.Fprintln(os.Stderr, "=====================================================")
}

func init() {
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR2)
		for range ch {
			live.mu.Lock()
			for id, pcs := range live.live {
				fmt.Fprintln(os.Stderr, "=====================================================")
				fmt.Fprintln(os.Stderr, "=== Live reference with id", id, "created from")
				summarizeStack(os.Stderr, pcs)
				fmt.Fprintln(os.Stderr, "=====================================================")
			}
			live.mu.Unlock()
		}
	}()
}

func summarizeStack(w io.Writer, pcs []uintptr) {
	frames := runtime.CallersFrames(pcs)
	for {
		frame, more := frames.Next()
		if !more {
			break
		}
		fmt.Fprintf(w, "    %s:%s:%d\n",
			frame.Function,
			filepath.Base(frame.File),
			frame.Line)
	}
}
