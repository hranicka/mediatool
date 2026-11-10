package run

import (
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// TmpGuard removes registered temporary paths when INT/TERM arrives and
// exits with 130, mirroring the bash scripts' interrupt trap.
type TmpGuard struct {
	mu    sync.Mutex
	paths []string
}

// NewTmpGuard starts the signal watcher.
func NewTmpGuard() *TmpGuard {
	g := &TmpGuard{}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		g.Cleanup()
		os.Exit(130)
	}()
	return g
}

// Add registers temporary paths (temp output files, frame directories) for
// signal cleanup.
func (g *TmpGuard) Add(paths ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paths = append(g.paths, paths...)
}

// Remove deregisters paths, e.g. after the output was renamed into place.
func (g *TmpGuard) Remove(paths ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range paths {
		for i, reg := range g.paths {
			if reg == p {
				g.paths = append(g.paths[:i], g.paths[i+1:]...)
				break
			}
		}
	}
}

// Cleanup removes every registered path (also usable in tests).
func (g *TmpGuard) Cleanup() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.paths {
		if err := os.RemoveAll(p); err != nil {
			slog.Debug("cannot remove temp path", "path", p, "err", err)
		}
	}
	g.paths = nil
}
