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

const filesystemWatchMask = unix.IN_CREATE |
	unix.IN_MODIFY |
	unix.IN_CLOSE_WRITE |
	unix.IN_MOVED_FROM |
	unix.IN_MOVED_TO |
	unix.IN_DELETE_SELF |
	unix.IN_MOVE_SELF |
	unix.IN_ONLYDIR |
	unix.IN_EXCL_UNLINK

const filesystemTriggerMask = unix.IN_CREATE |
	unix.IN_MODIFY |
	unix.IN_CLOSE_WRITE |
	unix.IN_MOVED_FROM |
	unix.IN_MOVED_TO |
	unix.IN_MOVE_SELF

type filesystemLibrary struct {
	name     domain.LibraryName
	root     string
	download bool
}

type filesystemWatch struct {
	path   string
	device uint64
	inode  uint64
}

type inotifyFilesystemWatcher struct {
	fd         int
	wakeRead   int
	completion *CompletionTracker
	triggers   chan<- ScanTrigger

	mu        sync.Mutex
	wdToWatch map[int]filesystemWatch
	dirToWD   map[string]int
	libraries map[domain.LibraryName]filesystemLibrary
}

func (s FilesystemEventSource) Run(ctx context.Context, cfgProvider ConfigProvider, triggers chan<- ScanTrigger) (runErr error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("initialize filesystem watcher: %w", err)
	}
	wakeFDs := []int{0, 0}
	if err := unix.Pipe2(wakeFDs, unix.O_CLOEXEC); err != nil {
		return errors.Join(fmt.Errorf("initialize filesystem watcher wake pipe: %w", err), closeFilesystemInotifyFD(fd))
	}
	defer func() {
		runErr = errors.Join(runErr, closeFilesystemWakeFD(wakeFDs[0]), closeFilesystemWakeFD(wakeFDs[1]))
	}()

	watcher := &inotifyFilesystemWatcher{
		fd:         fd,
		wakeRead:   wakeFDs[0],
		completion: s.Completion,
		triggers:   triggers,
		wdToWatch:  make(map[int]filesystemWatch),
		dirToWD:    make(map[string]int),
		libraries:  make(map[domain.LibraryName]filesystemLibrary),
	}
	watcher.reconcile(ctx, cfgProvider())

	readDone := make(chan error, 1)
	go func() {
		readDone <- watcher.readEvents(ctx)
	}()

	interval := s.ReconcileInterval
	if interval <= 0 {
		interval = DefaultConfigReconcileInterval
	}
	reconcileTimer := time.NewTimer(interval)
	defer reconcileTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			// Wake the reader through the pipe and wait for it to exit before
			// closing the inotify descriptor. Closing first could let another
			// goroutine reuse the descriptor number while the reader still has it.
			wakeErr := wakeFilesystemReader(wakeFDs[1])
			if wakeErr != nil {
				// A failed wake must not hang shutdown; closing the descriptor is
				// the only remaining chance to unblock the reader.
				wakeErr = errors.Join(wakeErr, closeFilesystemInotifyFD(fd))
			}
			readErr := <-readDone
			return errors.Join(ctx.Err(), wakeErr, closeFilesystemInotifyFD(fd), readErr)
		case readErr := <-readDone:
			if readErr == nil {
				readErr = errors.New("filesystem event reader stopped")
			}
			return errors.Join(readErr, closeFilesystemInotifyFD(fd))
		case <-reconcileTimer.C:
			watcher.reconcile(ctx, cfgProvider())
			reconcileTimer.Reset(interval)
		}
	}
}

func closeFilesystemInotifyFD(fd int) error {
	if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
		return fmt.Errorf("close filesystem watcher: %w", err)
	}
	return nil
}

func closeFilesystemWakeFD(fd int) error {
	if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
		return fmt.Errorf("close filesystem watcher wake pipe: %w", err)
	}
	return nil
}

func wakeFilesystemReader(fd int) error {
	if _, err := unix.Write(fd, []byte{1}); err != nil {
		return fmt.Errorf("wake filesystem watcher: %w", err)
	}
	return nil
}

func (w *inotifyFilesystemWatcher) readEvents(ctx context.Context) error {
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
			return fmt.Errorf("poll filesystem events: %w", err)
		}
		if pollFDs[1].Revents != 0 {
			// The parent closes the inotify descriptor only after this loop has
			// exited, so a reused descriptor number is never read.
			return nil
		}
		if pollFDs[0].Revents&unix.POLLIN == 0 {
			return fmt.Errorf("poll filesystem events returned flags %#x", pollFDs[0].Revents)
		}

		n, err := unix.Read(w.fd, buffer)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if ctx.Err() != nil && (errors.Is(err, unix.EBADF) || errors.Is(err, unix.EINVAL)) {
				return nil
			}
			return fmt.Errorf("read filesystem events: %w", err)
		}
		if n == 0 {
			return errors.New("filesystem event stream closed")
		}
		if err := w.parseEvents(ctx, buffer[:n]); err != nil {
			return err
		}
	}
}

func (w *inotifyFilesystemWatcher) parseEvents(ctx context.Context, buffer []byte) error {
	offset := 0
	for offset+unix.SizeofInotifyEvent <= len(buffer) {
		event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
		offset += unix.SizeofInotifyEvent
		nameEnd := offset + int(event.Len)
		if nameEnd > len(buffer) {
			return errors.New("parse filesystem event: truncated name")
		}
		name := string(bytes.TrimRight(buffer[offset:nameEnd], "\x00"))
		offset = nameEnd
		if !w.handleEvent(ctx, int(event.Wd), event.Mask, name) {
			return ctx.Err()
		}
	}
	if offset != len(buffer) {
		return errors.New("parse filesystem event: truncated event")
	}
	return nil
}

func (w *inotifyFilesystemWatcher) handleEvent(ctx context.Context, wd int, mask uint32, name string) bool {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		for _, libraryName := range w.libraryNames() {
			if !w.emit(ctx, ScanTrigger{LibraryName: libraryName, Reason: "filesystem"}) {
				return false
			}
		}
		return true
	}

	watch, ok := w.watchForDescriptor(wd)
	if mask&unix.IN_IGNORED != 0 {
		w.dropWatch(wd)
		return true
	}
	if !ok {
		return true
	}

	absPath := watch.path
	if name != "" {
		absPath = filepath.Join(watch.path, name)
	}
	absPath = filepath.Clean(absPath)
	isDir := mask&unix.IN_ISDIR != 0

	if mask&unix.IN_DELETE_SELF != 0 {
		w.dropWatch(wd)
		return true
	}
	if mask&unix.IN_MOVE_SELF != 0 {
		w.refreshMovedWatch(wd)
	}

	if isDir && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
		if _, err := w.addRecursive(ctx, absPath); err != nil && ctx.Err() == nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("add filesystem watches for new directory", "path", absPath, "error", err)
		}
	}
	if isDir && mask&unix.IN_MOVED_TO != 0 {
		return w.handleMovedInDirectory(ctx, absPath)
	}
	if mask&filesystemTriggerMask == 0 {
		return true
	}

	libraries := w.librariesFor(absPath)
	completionEvent := !isDir && mask&(unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO) != 0
	if completionEvent && w.completion != nil {
		for _, library := range libraries {
			if library.download {
				w.completion.Mark(absPath, time.Now().UTC())
				break
			}
		}
	}
	for _, library := range libraries {
		trigger := ScanTrigger{LibraryName: library.name, Reason: "filesystem", Path: absPath}
		if completionEvent && library.download && w.completion != nil {
			trigger.Reason = "transfer-complete"
			trigger.Completed = true
		}
		if !w.emit(ctx, trigger) {
			return false
		}
	}
	return true
}

func (w *inotifyFilesystemWatcher) handleMovedInDirectory(ctx context.Context, path string) bool {
	libraries := w.librariesForMovedDirectory(path)
	if w.completion != nil {
		markRoots := make(map[string]struct{})
		for _, library := range libraries {
			if !library.download {
				continue
			}
			root := path
			if pathWithinRoot(library.root, path) {
				root = library.root
			}
			markRoots[root] = struct{}{}
		}
		roots := make([]string, 0, len(markRoots))
		for root := range markRoots {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		markedAt := time.Now().UTC()
		for _, root := range roots {
			if err := w.markRegularFiles(ctx, root, markedAt); err != nil && ctx.Err() == nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("mark files in completed directory", "path", root, "error", err)
			}
		}
	}

	for _, library := range libraries {
		trigger := ScanTrigger{LibraryName: library.name, Reason: "filesystem", Path: path}
		if library.download && w.completion != nil {
			trigger.Reason = "transfer-complete"
			trigger.Completed = true
		}
		if !w.emit(ctx, trigger) {
			return false
		}
	}
	return true
}

func (w *inotifyFilesystemWatcher) reconcile(ctx context.Context, cfg config.Config) {
	names := make([]string, 0, len(cfg.Libraries))
	for name := range cfg.Libraries {
		names = append(names, name)
	}
	sort.Strings(names)

	next := make(map[domain.LibraryName]filesystemLibrary, len(names))
	for _, name := range names {
		library := cfg.Libraries[name]
		configured := filesystemLibrary{
			name:     domain.LibraryName(name),
			download: library.Kind == "download",
		}
		root := strings.TrimSpace(library.Path)
		if root != "" {
			absRoot, err := filepath.Abs(root)
			if err != nil {
				slog.Warn("resolve library filesystem watch path", "library", name, "path", root, "error", err)
			} else {
				configured.root = filepath.Clean(absRoot)
			}
		}
		next[configured.name] = configured
	}

	w.mu.Lock()
	w.libraries = next
	w.mu.Unlock()
	w.removeUnneededWatches(next)

	seenRoots := make(map[string]struct{}, len(next))
	for _, name := range names {
		library := next[domain.LibraryName(name)]
		if library.root == "" {
			continue
		}
		if _, ok := seenRoots[library.root]; ok {
			continue
		}
		seenRoots[library.root] = struct{}{}
		added, err := w.addRecursive(ctx, library.root)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("add library filesystem watches", "library", library.name, "path", library.root, "error", err)
			}
			continue
		}
		if !added {
			slog.Warn("library path unavailable for filesystem watch", "library", library.name, "path", library.root)
		}
	}
}

func (w *inotifyFilesystemWatcher) removeUnneededWatches(libraries map[domain.LibraryName]filesystemLibrary) {
	roots := make([]string, 0, len(libraries))
	for _, library := range libraries {
		if library.root != "" {
			roots = append(roots, library.root)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	for wd, watch := range w.wdToWatch {
		keep := false
		for _, root := range roots {
			if pathWithinRoot(watch.path, root) && filesystemWatchIdentityMatches(watch) {
				keep = true
				break
			}
		}
		if keep {
			continue
		}
		w.deleteWatchLocked(wd, watch)
		if _, err := unix.InotifyRmWatch(w.fd, uint32(wd)); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EBADF) {
			slog.Debug("remove filesystem watch", "path", watch.path, "error", err)
		}
	}
}

func (w *inotifyFilesystemWatcher) addRecursive(ctx context.Context, root string) (bool, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("filesystem watch path is not a directory")
	}

	addedAny := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("skip filesystem watch path", "path", path, "error", walkErr)
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
			slog.Warn("add filesystem watch", "path", path, "error", err)
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

func (w *inotifyFilesystemWatcher) addWatch(dir string) error {
	dir = filepath.Clean(dir)
	var stat unix.Stat_t
	if err := unix.Stat(dir, &stat); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if wd, ok := w.dirToWD[dir]; ok {
		watch := w.wdToWatch[wd]
		if watch.device == stat.Dev && watch.inode == stat.Ino {
			return nil
		}
		w.deleteWatchLocked(wd, watch)
		if _, err := unix.InotifyRmWatch(w.fd, uint32(wd)); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("replace stale filesystem watch: %w", err)
		}
	}

	wd, err := unix.InotifyAddWatch(w.fd, dir, filesystemWatchMask)
	if err != nil {
		return err
	}
	if old, ok := w.wdToWatch[wd]; ok && old.path != dir {
		delete(w.dirToWD, old.path)
	}
	w.wdToWatch[wd] = filesystemWatch{path: dir, device: stat.Dev, inode: stat.Ino}
	w.dirToWD[dir] = wd
	return nil
}

func (w *inotifyFilesystemWatcher) markRegularFiles(ctx context.Context, root string, at time.Time) error {
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
			w.completion.Mark(path, at)
		}
		return nil
	})
}

func (w *inotifyFilesystemWatcher) watchForDescriptor(wd int) (filesystemWatch, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	watch, ok := w.wdToWatch[wd]
	return watch, ok
}

func (w *inotifyFilesystemWatcher) dropWatch(wd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	watch, ok := w.wdToWatch[wd]
	if !ok {
		return
	}
	w.deleteWatchLocked(wd, watch)
}

func (w *inotifyFilesystemWatcher) refreshMovedWatch(wd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	watch, ok := w.wdToWatch[wd]
	if !ok || filesystemWatchIdentityMatches(watch) {
		return
	}
	w.deleteWatchLocked(wd, watch)
	if _, err := unix.InotifyRmWatch(w.fd, uint32(wd)); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EBADF) {
		slog.Debug("remove moved filesystem watch", "path", watch.path, "error", err)
	}
}

func (w *inotifyFilesystemWatcher) deleteWatchLocked(wd int, watch filesystemWatch) {
	delete(w.wdToWatch, wd)
	if currentWD, ok := w.dirToWD[watch.path]; ok && currentWD == wd {
		delete(w.dirToWD, watch.path)
	}
}

func filesystemWatchIdentityMatches(watch filesystemWatch) bool {
	var stat unix.Stat_t
	return unix.Stat(watch.path, &stat) == nil && watch.device == stat.Dev && watch.inode == stat.Ino
}

func (w *inotifyFilesystemWatcher) librariesFor(path string) []filesystemLibrary {
	w.mu.Lock()
	roots := make(map[domain.LibraryName]string, len(w.libraries))
	libraries := make(map[domain.LibraryName]filesystemLibrary, len(w.libraries))
	for name, library := range w.libraries {
		if library.root != "" {
			roots[name] = library.root
		}
		libraries[name] = library
	}
	w.mu.Unlock()

	names := librariesForPath(roots, path)
	matched := make([]filesystemLibrary, 0, len(names))
	for _, name := range names {
		matched = append(matched, libraries[name])
	}
	return matched
}

func (w *inotifyFilesystemWatcher) librariesForMovedDirectory(path string) []filesystemLibrary {
	w.mu.Lock()
	libraries := make([]filesystemLibrary, 0, len(w.libraries))
	for _, library := range w.libraries {
		if library.root == "" {
			continue
		}
		if pathWithinRoot(path, library.root) || pathWithinRoot(library.root, path) {
			libraries = append(libraries, library)
		}
	}
	w.mu.Unlock()
	sort.Slice(libraries, func(i, j int) bool {
		return libraries[i].name < libraries[j].name
	})
	return libraries
}

func (w *inotifyFilesystemWatcher) libraryNames() []domain.LibraryName {
	w.mu.Lock()
	names := make([]domain.LibraryName, 0, len(w.libraries))
	for name := range w.libraries {
		names = append(names, name)
	}
	w.mu.Unlock()
	sort.Slice(names, func(i, j int) bool {
		return names[i] < names[j]
	})
	return names
}

func (w *inotifyFilesystemWatcher) emit(ctx context.Context, trigger ScanTrigger) bool {
	select {
	case w.triggers <- trigger:
		return true
	case <-ctx.Done():
		return false
	}
}
