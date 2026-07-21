package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

func runForceOccurrenceCommand(ctx context.Context, cfg config.Config, opts options) error {
	library, ok := cfg.FindLibrary(domain.LibraryName(opts.libraryName))
	if !ok {
		return fmt.Errorf("library %q not found", opts.libraryName)
	}
	candidate, err := resolveForceOccurrenceCandidate(ctx, library, opts.forcePath)
	if err != nil {
		return err
	}

	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)
	result, err := forceOccurrence(ctx, state, library, candidate, time.Now().UTC())
	if err != nil {
		return err
	}
	return writeOutput(os.Stdout, func(w *outputWriter) {
		w.printf("library=%s path=%s source_id=%d source_generation=%d asset_id=%d asset_generation=%d job=%s id=%d state=%s\n",
			library.Name,
			candidate.LibraryRelativePath,
			result.Source.ID,
			result.Source.Generation,
			result.Asset.ID,
			result.Asset.Generation,
			result.Job.Label(),
			result.Job.ID,
			result.Job.State,
		)
	})
}

func resolveForceOccurrenceCandidate(ctx context.Context, library config.LibraryConfig, relativePath string) (scanner.CandidatePlan, error) {
	relativePath, err := cleanLibraryRelativePath(relativePath)
	if err != nil {
		return scanner.CandidatePlan{}, err
	}
	plan, err := (scanner.Scanner{}).PlanLibrary(ctx, library)
	if err != nil {
		return scanner.CandidatePlan{}, fmt.Errorf("plan library %q for forced occurrence: %w", library.Name, err)
	}
	var target scanner.CandidatePlan
	found := false
	for _, candidate := range plan.Candidates {
		if candidate.LibraryRelativePath != relativePath {
			continue
		}
		if found {
			return scanner.CandidatePlan{}, fmt.Errorf("force-occurrence target %q is ambiguous in library %q", relativePath, library.Name)
		}
		target = candidate
		found = true
	}
	if !found {
		return scanner.CandidatePlan{}, fmt.Errorf("force-occurrence target %q was not found in library %q", relativePath, library.Name)
	}
	if target.Ignored {
		return scanner.CandidatePlan{}, fmt.Errorf("force-occurrence target %q is ignored: %s", relativePath, target.IgnoreReason)
	}
	if target.Unstable {
		return scanner.CandidatePlan{}, fmt.Errorf("force-occurrence target %q is still unstable", relativePath)
	}
	if !target.Enqueueable {
		return scanner.CandidatePlan{}, fmt.Errorf("force-occurrence target %q is not enqueueable", relativePath)
	}
	return target, nil
}

func cleanLibraryRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("force-occurrence relative path is required")
	}
	if strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return "", fmt.Errorf("force-occurrence path %q must be relative to the library root", value)
	}
	cleaned := path.Clean(filepath.ToSlash(value))
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("force-occurrence path %q must stay within the library root", value)
	}
	return cleaned, nil
}

func forceOccurrence(ctx context.Context, state *store.SQLiteStore, library config.LibraryConfig, candidate scanner.CandidatePlan, now time.Time) (store.ForceOccurrenceResult, error) {
	result, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
		LibraryName:        domain.LibraryName(library.Name),
		SourceKind:         candidate.SourceKind,
		SourceRelativePath: candidate.SourceRelativePath,
		SourceFingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SourceSizeBytes,
			ModTime:   candidate.SourceModTime,
		},
		AssetRelativePath: candidate.AssetRelativePath,
		AssetRole:         candidate.Role,
		AssetFingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SizeBytes,
			ModTime:   candidate.ModTime,
		},
		Priority: library.Priority,
		Now:      now,
	})
	if err != nil {
		return store.ForceOccurrenceResult{}, fmt.Errorf("force occurrence for library %q path %q: %w", library.Name, candidate.LibraryRelativePath, err)
	}
	return result, nil
}
