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

type filesystemWatchAlias struct {
	path   string
	device uint64
	inode  uint64
}

type filesystemWatch struct {
	device  uint64
	inode   uint64
	aliases map[string]filesystemWatchAlias
}

type filesystemTriggerBatch map[domain.LibraryName]ScanTrigger

type inotifyFilesystemWatcher struct {
	fd                int
	wakeRead          int
	completion        *CompletionTracker
	triggers          chan<- ScanTrigger
	reconcileRequests chan<- struct{}
	triggerReady      chan struct{}

	mu             sync.Mutex
	wdToWatch      map[int]*filesystemWatch
	dirToWD        map[string]int
	ignoredPending map[int]int
	libraries      map[domain.LibraryName]filesystemLibrary

	triggerMu       sync.Mutex
	pendingTriggers map[domain.LibraryName]ScanTrigger
}

func (s FilesystemEventSource) Run(ctx context.Context, cfgProvider ConfigProvider, triggers chan<- ScanTrigger) (runErr error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
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

	reconcileRequests := make(chan struct{}, 1)
	watcher := &inotifyFilesystemWatcher{
		fd:                fd,
		wakeRead:          wakeFDs[0],
		completion:        s.Completion,
		triggers:          triggers,
		reconcileRequests: reconcileRequests,
		triggerReady:      make(chan struct{}, 1),
		wdToWatch:         make(map[int]*filesystemWatch),
		dirToWD:           make(map[string]int),
		ignoredPending:    make(map[int]int),
		libraries:         make(map[domain.LibraryName]filesystemLibrary),
		pendingTriggers:   make(map[domain.LibraryName]ScanTrigger),
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watcher.reconcile(runCtx, cfgProvider())

	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		watcher.dispatchTriggers(runCtx)
	}()

	readDone := make(chan error, 1)
	go func() {
		readDone <- watcher.readEvents(runCtx)
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
			cancel()
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
			<-dispatchDone
			return errors.Join(ctx.Err(), wakeErr, closeFilesystemInotifyFD(fd), readErr)
		case readErr := <-readDone:
			cancel()
			<-dispatchDone
			if readErr == nil {
				readErr = errors.New("filesystem event reader stopped")
			}
			return errors.Join(readErr, closeFilesystemInotifyFD(fd))
		case <-reconcileRequests:
			stopTimer(reconcileTimer)
			watcher.reconcile(runCtx, cfgProvider())
			reconcileTimer.Reset(interval)
		case <-reconcileTimer.C:
			watcher.reconcile(runCtx, cfgProvider())
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

		batch := make(filesystemTriggerBatch)
		for {
			// A continuously busy inotify fd may never reach EAGAIN. Check
			// cancellation between reads so shutdown cannot be starved by an
			// event producer that fills the queue as quickly as it is drained.
			if ctx.Err() != nil {
				return nil
			}
			n, err := unix.Read(w.fd, buffer)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.EAGAIN) {
				break
			}
			if err != nil {
				if ctx.Err() != nil && (errors.Is(err, unix.EBADF) || errors.Is(err, unix.EINVAL)) {
					return nil
				}
				return fmt.Errorf("read filesystem events: %w", err)
			}
			if n == 0 {
				return errors.New("filesystem event stream closed")
			}
			if err := w.parseEvents(ctx, buffer[:n], batch); err != nil {
				w.publishTriggers(batch)
				return err
			}
		}
		// Publish only after draining events already queued in the kernel. This
		// lets a later mutation invalidate an earlier close before a scan can use
		// the completion confidence, while dispatch remains free to block.
		w.publishTriggers(batch)
	}
}

func (w *inotifyFilesystemWatcher) parseEvents(ctx context.Context, buffer []byte, batch filesystemTriggerBatch) error {
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
		w.handleEvent(ctx, int(event.Wd), event.Mask, name, batch)
	}
	if offset != len(buffer) {
		return errors.New("parse filesystem event: truncated event")
	}
	return nil
}

func (w *inotifyFilesystemWatcher) handleEvent(ctx context.Context, wd int, mask uint32, name string, batch filesystemTriggerBatch) {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		slog.Warn("filesystem event queue overflow", "action", "clear completion confidence and reconcile watches")
		w.completion.Reset()
		w.clearPendingTriggers()
		w.requestReconcile()
		for _, libraryName := range w.libraryNames() {
			// Ordering was lost, so replace stronger completion events from this
			// batch with a full-library scan that cannot bypass stability.
			batch[libraryName] = ScanTrigger{LibraryName: libraryName, Reason: "filesystem"}
		}
		return
	}
	if mask&unix.IN_IGNORED != 0 {
		w.handleIgnored(wd)
		return
	}

	aliases := w.aliasesForDescriptor(wd)
	if len(aliases) == 0 {
		return
	}
	targets := filesystemEventPaths(aliases, name)
	isDir := mask&unix.IN_ISDIR != 0

	// Mutation events are ordered after close events on the same inotify fd.
	// Invalidate every lexical alias before any trigger from this event can be
	// published; a later close will establish confidence again.
	if !isDir && mask&(unix.IN_CREATE|unix.IN_MODIFY|unix.IN_MOVED_FROM) != 0 {
		for _, path := range targets {
			w.completion.Invalidate(path)
		}
	}

	if mask&unix.IN_DELETE_SELF != 0 {
		w.pruneStaleAliases(aliases)
		return
	}
	if mask&unix.IN_MOVE_SELF != 0 {
		w.pruneStaleAliases(aliases)
	}

	if isDir && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
		for _, path := range targets {
			if _, err := w.addRecursive(ctx, path); err != nil && ctx.Err() == nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("add filesystem watches for new directory", "path", path, "error", err)
			}
		}
	}
	if isDir && mask&unix.IN_MOVED_TO != 0 {
		for _, path := range targets {
			w.handleMovedInDirectory(ctx, path, batch)
		}
		return
	}
	if mask&filesystemTriggerMask == 0 {
		return
	}

	completionEvent := !isDir && mask&(unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO) != 0
	markedAt := time.Now().UTC()
	for _, path := range targets {
		libraries := w.librariesFor(path)
		if completionEvent {
			for _, library := range libraries {
				if library.download {
					w.completion.Mark(path, markedAt)
					break
				}
			}
		}
		for _, library := range libraries {
			trigger := ScanTrigger{LibraryName: library.name, Reason: "filesystem", Path: path}
			if completionEvent && library.download && w.completion != nil {
				trigger.Reason = "transfer-complete"
				trigger.Completed = true
			}
			queueFilesystemTrigger(batch, trigger)
		}
	}
}

func (w *inotifyFilesystemWatcher) handleMovedInDirectory(ctx context.Context, path string, batch filesystemTriggerBatch) {
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
		queueFilesystemTrigger(batch, trigger)
	}
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

	w.replaceLibraries(next)
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

func (w *inotifyFilesystemWatcher) replaceLibraries(libraries map[domain.LibraryName]filesystemLibrary) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.triggerMu.Lock()
	defer w.triggerMu.Unlock()

	w.libraries = libraries
	for name := range w.pendingTriggers {
		if _, ok := libraries[name]; !ok {
			delete(w.pendingTriggers, name)
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
	sort.Strings(roots)

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, wd := range sortedWatchDescriptors(w.wdToWatch) {
		watch := w.wdToWatch[wd]
		for _, alias := range sortedWatchAliases(watch) {
			keep := filesystemWatchIdentityMatches(alias)
			if keep {
				keep = false
				for _, root := range roots {
					if pathWithinRoot(alias.path, root) {
						keep = true
						break
					}
				}
			}
			if !keep {
				w.deleteAliasLocked(wd, alias.path)
			}
		}
		if len(watch.aliases) == 0 {
			if err := w.removeEmptyWatchLocked(wd); err != nil {
				slog.Debug("remove unneeded filesystem watch", "wd", wd, "error", err)
			}
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
	alias := filesystemWatchAlias{path: dir, device: stat.Dev, inode: stat.Ino}

	w.mu.Lock()
	defer w.mu.Unlock()
	oldWD := -1
	if wd, ok := w.dirToWD[dir]; ok {
		watch := w.wdToWatch[wd]
		if watch != nil {
			if current, exists := watch.aliases[dir]; exists && current.device == stat.Dev && current.inode == stat.Ino {
				return nil
			}
			w.deleteAliasLocked(wd, dir)
		} else {
			delete(w.dirToWD, dir)
		}
		oldWD = wd
	}

	wd, err := unix.InotifyAddWatch(w.fd, dir, filesystemWatchMask)
	if err != nil {
		if oldWD >= 0 {
			err = errors.Join(err, w.removeEmptyWatchLocked(oldWD))
		}
		return err
	}

	watch := w.wdToWatch[wd]
	if watch != nil && (watch.device != stat.Dev || watch.inode != stat.Ino) {
		// The kernel reused a descriptor whose automatic IN_IGNORED is still
		// queued. Retire only the stale userspace aliases and consume that old
		// notification without dropping the newly installed watch.
		w.deleteWatchStateLocked(wd)
		w.ignoredPending[wd]++
		watch = nil
	}
	if watch == nil {
		watch = &filesystemWatch{
			device:  stat.Dev,
			inode:   stat.Ino,
			aliases: make(map[string]filesystemWatchAlias),
		}
		w.wdToWatch[wd] = watch
	}
	watch.aliases[dir] = alias
	w.dirToWD[dir] = wd

	if oldWD >= 0 && oldWD != wd {
		if err := w.removeEmptyWatchLocked(oldWD); err != nil {
			return fmt.Errorf("remove replaced filesystem watch: %w", err)
		}
	}
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

func (w *inotifyFilesystemWatcher) aliasesForDescriptor(wd int) []filesystemWatchAlias {
	w.mu.Lock()
	defer w.mu.Unlock()
	return sortedWatchAliases(w.wdToWatch[wd])
}

func (w *inotifyFilesystemWatcher) handleIgnored(wd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if pending := w.ignoredPending[wd]; pending > 0 {
		if pending == 1 {
			delete(w.ignoredPending, wd)
		} else {
			w.ignoredPending[wd] = pending - 1
		}
		return
	}
	w.deleteWatchStateLocked(wd)
}

func (w *inotifyFilesystemWatcher) pruneStaleAliases(eventAliases []filesystemWatchAlias) {
	staleRoots := make([]string, 0, len(eventAliases))
	for _, alias := range eventAliases {
		if !filesystemWatchIdentityMatches(alias) {
			staleRoots = append(staleRoots, alias.path)
		}
	}
	if len(staleRoots) == 0 {
		return
	}
	sort.Strings(staleRoots)

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, wd := range sortedWatchDescriptors(w.wdToWatch) {
		watch := w.wdToWatch[wd]
		for _, alias := range sortedWatchAliases(watch) {
			underStaleRoot := false
			for _, root := range staleRoots {
				if pathWithinRoot(alias.path, root) {
					underStaleRoot = true
					break
				}
			}
			if underStaleRoot && !filesystemWatchIdentityMatches(alias) {
				w.deleteAliasLocked(wd, alias.path)
			}
		}
		if len(watch.aliases) == 0 {
			if err := w.removeEmptyWatchLocked(wd); err != nil {
				slog.Debug("remove stale moved filesystem watch", "wd", wd, "error", err)
			}
		}
	}
}

func (w *inotifyFilesystemWatcher) deleteAliasLocked(wd int, path string) {
	watch := w.wdToWatch[wd]
	if watch != nil {
		delete(watch.aliases, path)
	}
	if currentWD, ok := w.dirToWD[path]; ok && currentWD == wd {
		delete(w.dirToWD, path)
	}
}

func (w *inotifyFilesystemWatcher) deleteWatchStateLocked(wd int) {
	watch := w.wdToWatch[wd]
	if watch == nil {
		return
	}
	for path := range watch.aliases {
		if currentWD, ok := w.dirToWD[path]; ok && currentWD == wd {
			delete(w.dirToWD, path)
		}
	}
	delete(w.wdToWatch, wd)
}

func (w *inotifyFilesystemWatcher) removeEmptyWatchLocked(wd int) error {
	watch := w.wdToWatch[wd]
	if watch == nil || len(watch.aliases) != 0 {
		return nil
	}
	delete(w.wdToWatch, wd)
	_, err := unix.InotifyRmWatch(w.fd, uint32(wd))
	if err == nil || errors.Is(err, unix.EINVAL) {
		// IN_IGNORED is queued by both explicit and automatic removal. Remember
		// it in case the kernel reuses wd before the reader reaches that event.
		w.ignoredPending[wd]++
		return nil
	}
	if errors.Is(err, unix.EBADF) {
		return nil
	}
	return err
}

func filesystemWatchIdentityMatches(alias filesystemWatchAlias) bool {
	var stat unix.Stat_t
	return unix.Stat(alias.path, &stat) == nil && alias.device == stat.Dev && alias.inode == stat.Ino
}

func sortedWatchDescriptors(watches map[int]*filesystemWatch) []int {
	wds := make([]int, 0, len(watches))
	for wd := range watches {
		wds = append(wds, wd)
	}
	sort.Ints(wds)
	return wds
}

func sortedWatchAliases(watch *filesystemWatch) []filesystemWatchAlias {
	if watch == nil {
		return nil
	}
	aliases := make([]filesystemWatchAlias, 0, len(watch.aliases))
	for _, alias := range watch.aliases {
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool {
		return aliases[i].path < aliases[j].path
	})
	return aliases
}

func librariesForPath(roots map[domain.LibraryName]string, path string) []domain.LibraryName {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	absPath = filepath.Clean(absPath)
	names := make([]domain.LibraryName, 0, len(roots))
	for name, root := range roots {
		if pathWithinRoot(absPath, root) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return names[i] < names[j]
	})
	return names
}

func pathWithinRoot(path string, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func filesystemEventPaths(aliases []filesystemWatchAlias, name string) []string {
	unique := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		path := alias.path
		if name != "" {
			path = filepath.Join(path, name)
		}
		unique[filepath.Clean(path)] = struct{}{}
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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

func queueFilesystemTrigger(batch filesystemTriggerBatch, trigger ScanTrigger) {
	if trigger.LibraryName == "" {
		return
	}
	current, exists := batch[trigger.LibraryName]
	if !exists {
		batch[trigger.LibraryName] = trigger
		return
	}
	batch[trigger.LibraryName] = strongerFilesystemTrigger(current, trigger)
}

func strongerFilesystemTrigger(current ScanTrigger, next ScanTrigger) ScanTrigger {
	if current.Completed != next.Completed {
		if next.Completed {
			return next
		}
		return current
	}
	// A pathless trigger requests a whole-library scan (not a stability delay),
	// so it is stronger than a path-specific ordinary event after overflow.
	if !current.Completed && (current.Path == "") != (next.Path == "") {
		if next.Path == "" {
			return next
		}
		return current
	}
	if next.Path < current.Path {
		return next
	}
	return current
}

func (w *inotifyFilesystemWatcher) publishTriggers(batch filesystemTriggerBatch) {
	if len(batch) == 0 {
		return
	}

	w.mu.Lock()
	w.triggerMu.Lock()
	published := false
	for name, trigger := range batch {
		if _, configured := w.libraries[name]; !configured {
			continue
		}
		current, exists := w.pendingTriggers[name]
		if exists {
			trigger = strongerFilesystemTrigger(current, trigger)
		}
		w.pendingTriggers[name] = trigger
		published = true
	}
	w.triggerMu.Unlock()
	w.mu.Unlock()
	if !published {
		return
	}
	select {
	case w.triggerReady <- struct{}{}:
	default:
	}
}

func (w *inotifyFilesystemWatcher) dispatchTriggers(ctx context.Context) {
	for {
		trigger, ok := w.takePendingTrigger()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-w.triggerReady:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case w.triggers <- trigger:
		}
	}
}

func (w *inotifyFilesystemWatcher) takePendingTrigger() (ScanTrigger, bool) {
	w.triggerMu.Lock()
	defer w.triggerMu.Unlock()
	if len(w.pendingTriggers) == 0 {
		return ScanTrigger{}, false
	}
	names := make([]domain.LibraryName, 0, len(w.pendingTriggers))
	for name := range w.pendingTriggers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return names[i] < names[j]
	})
	name := names[0]
	trigger := w.pendingTriggers[name]
	delete(w.pendingTriggers, name)
	return trigger, true
}

func (w *inotifyFilesystemWatcher) clearPendingTriggers() {
	w.triggerMu.Lock()
	defer w.triggerMu.Unlock()
	clear(w.pendingTriggers)
}

func (w *inotifyFilesystemWatcher) requestReconcile() {
	select {
	case w.reconcileRequests <- struct{}{}:
	default:
	}
}
