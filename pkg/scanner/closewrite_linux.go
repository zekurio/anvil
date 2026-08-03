//go:build linux

package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"golang.org/x/sys/unix"
)

const closeWriteWatchMask = unix.IN_CLOSE_WRITE |
	unix.IN_MOVED_TO |
	unix.IN_CREATE |
	unix.IN_DELETE_SELF |
	unix.IN_MOVE_SELF |
	unix.IN_ONLYDIR |
	unix.IN_EXCL_UNLINK

type closeWriteWatcher struct {
	fd       int
	wakeRead int
	tracker  *CompletionTracker
	triggers chan<- ScanTrigger
	mu       sync.Mutex
	wdToDir  map[int]string
	dirToWD  map[string]int
	roots    map[domain.LibraryName]string
}

func runCloseWriteWatcher(ctx context.Context, cfgProvider ConfigProvider, triggers chan<- ScanTrigger, tracker *CompletionTracker, reconcileInterval time.Duration) (runErr error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("initialize close-write watcher: %w", err)
	}
	wakeFDs := []int{0, 0}
	if err := unix.Pipe2(wakeFDs, unix.O_CLOEXEC); err != nil {
		return errors.Join(fmt.Errorf("initialize close-write wake pipe: %w", err), closeInotifyFD(fd))
	}
	defer func() {
		runErr = errors.Join(runErr, closeWakeFD(wakeFDs[0]), closeWakeFD(wakeFDs[1]))
	}()
	watcher := &closeWriteWatcher{
		fd:       fd,
		wakeRead: wakeFDs[0],
		tracker:  tracker,
		triggers: triggers,
		wdToDir:  make(map[int]string),
		dirToWD:  make(map[string]int),
		roots:    make(map[domain.LibraryName]string),
	}
	watcher.reconcile(ctx, cfgProvider())

	readDone := make(chan error, 1)
	go func() {
		readDone <- watcher.readEvents(ctx)
	}()

	if reconcileInterval <= 0 {
		reconcileInterval = DefaultConfigReconcileInterval
	}
	reconcileTimer := time.NewTimer(reconcileInterval)
	defer reconcileTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			// Closing the inotify descriptor makes the read loop's next read
			// return EBADF. The pipe releases its blocking poll because Linux
			// does not reliably wake a read when another goroutine closes it.
			closeErr := closeInotifyFD(fd)
			wakeErr := wakeInotifyReader(wakeFDs[1])
			readErr := <-readDone
			return errors.Join(ctx.Err(), closeErr, wakeErr, readErr)
		case readErr := <-readDone:
			return errors.Join(readErr, closeInotifyFD(fd))
		case <-reconcileTimer.C:
			watcher.reconcile(ctx, cfgProvider())
			reconcileTimer.Reset(reconcileInterval)
		}
	}
}

func closeInotifyFD(fd int) error {
	if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
		return fmt.Errorf("close close-write watcher: %w", err)
	}
	return nil
}

func closeWakeFD(fd int) error {
	if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
		return fmt.Errorf("close close-write wake pipe: %w", err)
	}
	return nil
}

func wakeInotifyReader(fd int) error {
	if _, err := unix.Write(fd, []byte{1}); err != nil {
		return fmt.Errorf("wake close-write watcher: %w", err)
	}
	return nil
}

func (w *closeWriteWatcher) readEvents(ctx context.Context) error {
	buffer := make([]byte, 64*1024)
	pollFDs := []unix.PollFd{
		{Fd: int32(w.fd), Events: unix.POLLIN},
		{Fd: int32(w.wakeRead), Events: unix.POLLIN},
	}
	for {
		if _, err := unix.Poll(pollFDs, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("poll close-write events: %w", err)
		}
		if pollFDs[1].Revents != 0 {
			if _, err := unix.Read(w.fd, buffer); ctx.Err() != nil && errors.Is(err, unix.EBADF) {
				return nil
			}
			return fmt.Errorf("close-write wake did not close inotify descriptor")
		}

		n, err := unix.Read(w.fd, buffer)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if ctx.Err() != nil && (errors.Is(err, unix.EBADF) || errors.Is(err, unix.EINVAL)) {
				return nil
			}
			return fmt.Errorf("read close-write events: %w", err)
		}
		if n == 0 {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		if err := w.parseEvents(ctx, buffer[:n]); err != nil {
			return err
		}
	}
}

func (w *closeWriteWatcher) parseEvents(ctx context.Context, buffer []byte) error {
	for offset := 0; offset+unix.SizeofInotifyEvent <= len(buffer); {
		event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
		offset += unix.SizeofInotifyEvent
		nameEnd := offset + int(event.Len)
		if nameEnd > len(buffer) {
			return fmt.Errorf("parse close-write event: truncated name")
		}
		name := string(bytes.TrimRight(buffer[offset:nameEnd], "\x00"))
		offset = nameEnd
		w.handleEvent(ctx, int(event.Wd), event.Mask, name)
	}
	return nil
}

func (w *closeWriteWatcher) handleEvent(ctx context.Context, wd int, mask uint32, name string) {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		for _, libraryName := range w.libraryNames() {
			if !w.emit(ctx, ScanTrigger{LibraryName: libraryName, Reason: "filesystem"}) {
				return
			}
		}
		return
	}

	dir := w.directoryForWatch(wd)
	if mask&(unix.IN_IGNORED|unix.IN_DELETE_SELF) != 0 {
		w.dropWatch(wd)
	}
	if dir == "" || mask&unix.IN_IGNORED != 0 {
		return
	}

	absPath := dir
	if name != "" {
		absPath = filepath.Join(dir, name)
	}
	absPath = filepath.Clean(absPath)
	isDir := mask&unix.IN_ISDIR != 0
	if isDir && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
		if _, err := w.addRecursive(ctx, absPath); err != nil && ctx.Err() == nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("add close-write watches for new directory", "path", absPath, "error", err)
		}
	}

	if isDir && mask&unix.IN_MOVED_TO != 0 {
		if err := w.markRegularFiles(ctx, absPath); err != nil && ctx.Err() == nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("mark files in completed directory", "path", absPath, "error", err)
		}
		for _, libraryName := range w.librariesFor(absPath) {
			if !w.emit(ctx, ScanTrigger{LibraryName: libraryName, Reason: "transfer-complete", Path: absPath, Completed: true}) {
				return
			}
		}
		return
	}
	if isDir || mask&(unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO) == 0 {
		return
	}

	w.tracker.Mark(absPath, time.Now().UTC())
	for _, libraryName := range w.librariesFor(absPath) {
		if !w.emit(ctx, ScanTrigger{LibraryName: libraryName, Reason: "transfer-complete", Path: absPath, Completed: true}) {
			return
		}
	}
}

func (w *closeWriteWatcher) reconcile(ctx context.Context, cfg config.Config) {
	nextRoots := make(map[domain.LibraryName]string)
	for name, library := range cfg.Libraries {
		if library.Kind != "download" {
			continue
		}
		root := strings.TrimSpace(library.Path)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			slog.Warn("resolve close-write watch path", "library", name, "path", root, "error", err)
			continue
		}
		nextRoots[domain.LibraryName(name)] = filepath.Clean(absRoot)
	}

	w.mu.Lock()
	previousRoots := w.roots
	w.roots = nextRoots
	w.mu.Unlock()
	w.removeUnneededWatches(nextRoots)

	seenRoots := make(map[string]struct{}, len(nextRoots))
	for libraryName, root := range nextRoots {
		if _, ok := seenRoots[root]; ok {
			continue
		}
		seenRoots[root] = struct{}{}
		if previousRoots[libraryName] == root && w.isWatched(root) {
			continue
		}
		added, err := w.addRecursive(ctx, root)
		if err != nil {
			slog.Warn("add library close-write watches", "library", libraryName, "path", root, "error", err)
			continue
		}
		if !added {
			slog.Warn("download library path unavailable for close-write watch", "library", libraryName, "path", root)
		}
	}
}

func (w *closeWriteWatcher) removeUnneededWatches(roots map[domain.LibraryName]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for dir, wd := range w.dirToWD {
		keep := false
		for _, root := range roots {
			if pathWithinRoot(dir, root) {
				if info, err := os.Stat(dir); err == nil && info.IsDir() {
					keep = true
				}
				break
			}
		}
		if keep {
			continue
		}
		delete(w.dirToWD, dir)
		delete(w.wdToDir, wd)
		if _, err := unix.InotifyRmWatch(w.fd, uint32(wd)); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EBADF) {
			slog.Debug("remove close-write watch", "path", dir, "error", err)
		}
	}
}

func (w *closeWriteWatcher) addRecursive(ctx context.Context, root string) (bool, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("close-write watch path is not a directory")
	}

	addedAny := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("skip close-write watch path", "path", path, "error", walkErr)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if err := w.addWatch(path); err != nil {
			slog.Warn("add close-write watch", "path", path, "error", err)
			return filepath.SkipDir
		}
		addedAny = true
		return nil
	})
	if err != nil {
		return addedAny, err
	}
	return addedAny, nil
}

func (w *closeWriteWatcher) addWatch(dir string) error {
	dir = filepath.Clean(dir)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.dirToWD[dir]; ok {
		return nil
	}
	wd, err := unix.InotifyAddWatch(w.fd, dir, closeWriteWatchMask)
	if err != nil {
		return err
	}
	if oldDir, ok := w.wdToDir[wd]; ok && oldDir != dir {
		delete(w.dirToWD, oldDir)
	}
	w.wdToDir[wd] = dir
	w.dirToWD[dir] = wd
	return nil
}

func (w *closeWriteWatcher) markRegularFiles(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			w.tracker.Mark(path, time.Now().UTC())
		}
		return nil
	})
}

func (w *closeWriteWatcher) directoryForWatch(wd int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wdToDir[wd]
}

func (w *closeWriteWatcher) dropWatch(wd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	dir, ok := w.wdToDir[wd]
	if !ok {
		return
	}
	delete(w.wdToDir, wd)
	delete(w.dirToWD, dir)
}

func (w *closeWriteWatcher) isWatched(dir string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.dirToWD[filepath.Clean(dir)]
	return ok
}

func (w *closeWriteWatcher) librariesFor(path string) []domain.LibraryName {
	w.mu.Lock()
	roots := make(map[domain.LibraryName]string, len(w.roots))
	for name, root := range w.roots {
		roots[name] = root
	}
	w.mu.Unlock()
	return librariesForPath(roots, path)
}

func (w *closeWriteWatcher) libraryNames() []domain.LibraryName {
	w.mu.Lock()
	names := make([]domain.LibraryName, 0, len(w.roots))
	for name := range w.roots {
		names = append(names, name)
	}
	w.mu.Unlock()
	sort.Slice(names, func(i, j int) bool {
		return names[i] < names[j]
	})
	return names
}

func (w *closeWriteWatcher) emit(ctx context.Context, trigger ScanTrigger) bool {
	select {
	case w.triggers <- trigger:
		return true
	case <-ctx.Done():
		return false
	}
}
