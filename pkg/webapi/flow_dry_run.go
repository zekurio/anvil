package webapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/audio"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/flowstep"
	"github.com/zekurio/anvil/pkg/subtitle"
)

// Placeholders the dry-run command carries for what only a real run knows.
const (
	crfPlaceholder    = "{crf}"
	outputPlaceholder = "{output}"
)

// defaultProbeTimeout bounds the probe of a dry run. A probe decodes the
// first frames of the video, which takes seconds on a slow disk, not minutes.
const defaultProbeTimeout = time.Minute

// testFlow dry-runs a draft against one real file: it probes the file,
// evaluates the graph, and builds the command the encode step would run.
// Nothing is encoded and nothing is written.
func (s *Server) testFlow(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	var request FlowTestRequest
	if err := decodeBody(r, &request); err != nil {
		return fail(err)
	}
	graph := normalizeGraph(request.Graph)
	result := FlowTest{
		Check: s.flowCheck(graph), Path: []string{}, Decisions: []string{}, Plan: []string{}, Command: []string{},
		File: FileFacts{Audio: []StreamFacts{}, Subtitles: []StreamFacts{}},
	}
	input, resolved, err := s.dryRunInput(request.Path)
	if err != nil {
		return fail(err)
	}
	result.File.Path = input
	// A graph with errors has no path to walk. The check says what to fix, and
	// the rest stays empty.
	if !result.Check.Valid {
		return ok(result)
	}

	timeout := s.ProbeTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	// The probe reads the file the checks above resolved, not whatever the
	// name points at by now.
	probed, err := s.prober().Probe(probeCtx, resolved)
	switch {
	case r.Context().Err() != nil:
		return fail(r.Context().Err())
	case errors.Is(probeCtx.Err(), context.DeadlineExceeded):
		return fail(invalidf("path", "Probing %s took longer than %s and was stopped.", input, durationText(timeout)))
	case err != nil:
		return fail(invalidf("path", "%s", sentence(fmt.Sprintf("%s could not be probed: %v", input, err))))
	}
	probed.Path = input
	result.File = fileFacts(probed)

	// A real job asks the library's Arr connection for the original language
	// before it evaluates the flow. A dry run talks to nobody, so it evaluates
	// as a job would whose lookup found nothing.
	metadata := domain.JobMetadata{}
	resolution, err := flow.Evaluate(graph, flow.Facts{Probe: probed, Metadata: metadata})
	if err != nil {
		return fail(err)
	}
	result.Outcome = resolution.Outcome
	result.Path = resolution.Path
	result.Decisions = resolution.Decisions
	result.Plan = resolution.Plan
	if resolution.Outcome == flow.OutcomeSkip {
		return ok(result)
	}

	profile := resolution.Profile
	flowstep.DisableUnsafeStreamCleanup(profile, &metadata)
	if metadata.StreamCleanupDisabled {
		result.Plan = append(result.Plan, "Original language: not looked up in a test, so every track is kept here")
	}
	command, notes, err := s.dryRunCommand(profile, input, probed, metadata)
	if err != nil {
		// The flow is valid and resolved, but this file cannot be encoded with
		// it. That is a result of the test, not a failed request.
		result.Plan = append(result.Plan, "Encode: a job would fail here: "+err.Error())
		return ok(result)
	}
	result.Plan = append(result.Plan, notes...)
	result.Command = command
	return ok(result)
}

// dryRunInput checks the file a dry run names and returns its cleaned path
// and the path its links resolve to.
// Only a regular file below a library root is accepted, with links resolved:
// the route makes the daemon open and decode what it is pointed at, and that
// must stay inside what the operator handed to Anvil.
func (s *Server) dryRunInput(requested string) (input, resolved string, err error) {
	requested = strings.TrimSpace(requested)
	switch {
	case requested == "":
		return "", "", invalidf("path", "Choose a file to test the flow against.")
	case !filepath.IsAbs(requested):
		return "", "", invalidf("path", "The path must be absolute, such as /srv/media/film.mkv.")
	}
	input = filepath.Clean(requested)
	outside := invalidf("path", "%s is not inside a library. A flow can only be tested against a file of a configured library.", input)
	libraries := s.Config().Libraries
	// Asked before the filesystem is asked about the path: one outside every
	// library gets one answer whether it exists or not, so the route does not
	// tell what else is on the machine. A root may be spelled either way.
	named := insideLibrary(libraries, input)
	for _, library := range libraries {
		named = named || below(filepath.Clean(library.Path), input)
	}
	if !named {
		return "", "", outside
	}
	resolved, err = filepath.EvalSymlinks(input)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "", invalidf("path", "%s does not exist.", input)
	case errors.Is(err, fs.ErrPermission):
		return "", "", invalidf("path", "The daemon is not allowed to read %s.", input)
	case err != nil:
		return "", "", invalidf("path", "%s", sentence(fmt.Sprintf("%s cannot be read: %v", input, err)))
	}
	if !insideLibrary(libraries, resolved) {
		return "", "", outside
	}
	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return "", "", invalidf("path", "%s", sentence(fmt.Sprintf("%s cannot be read: %v", input, err)))
	case !info.Mode().IsRegular():
		return "", "", invalidf("path", "%s is not a file.", input)
	}
	return input, resolved, nil
}

// insideLibrary reports whether a path lies below the resolved root of any
// library.
func insideLibrary(libraries map[string]config.LibraryConfig, resolved string) bool {
	for _, library := range libraries {
		root, err := filepath.EvalSymlinks(library.Path)
		if err != nil {
			continue
		}
		if below(root, resolved) {
			return true
		}
	}
	return false
}

// below reports whether path lies strictly below root. Both are clean.
func below(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// dryRunCommand builds the ffmpeg argument list the encode step would run for
// the profile, the way the pipeline builds it: the audio and subtitle steps
// select tracks, then the encode step plans. What only a run can know is left
// as a placeholder, and the notes say which parts those are.
func (s *Server) dryRunCommand(profile domain.Profile, input string, probed domain.ProbeResult, metadata domain.JobMetadata) (command, notes []string, err error) {
	audioSelection, err := audio.Selector{}.Select(&probed, profile.Audio, metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("audio: %w", err)
	}
	subtitleSelection, err := subtitle.Selector{}.Select(&probed, profile.Subtitles, metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("subtitles: %w", err)
	}
	daemon := s.Config().Daemon
	// The thread count is the share the scheduler gives a job when every
	// worker is busy; a job that starts on an idle daemon may get more.
	threads := daemon.MaxThreadsPerJob
	if threads <= 0 && daemon.TotalThreads > 0 {
		threads = 1 + (daemon.TotalThreads-1)/max(daemon.WorkerCount, 1)
	}
	plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{
		Profile:      profile,
		RenderDevice: daemon.RenderDevice,
		InputPath:    input,
		OutputPath:   outputPlaceholder,
		Resources:    domain.ResourceAllocation{Threads: threads},
		Audio:        &audioSelection,
		Subtitles:    &subtitleSelection,
		Metadata:     metadata,
		Probe:        &probed,
	})
	if err != nil {
		return nil, nil, err
	}
	command = ffmpeg.Args(plan)
	switch {
	case plan.VideoCopy && !profile.Video.SkipEncode:
		notes = append(notes, "Video: copied after all, because "+plan.VideoCopyReason)
	case plan.VideoCopy:
	default:
		if plan.QualityMode != domain.VideoQualityFixed {
			replaceQuality(command)
			notes = append(notes, fmt.Sprintf("CRF: found by the quality search when the job runs, shown as %s", crfPlaceholder))
		}
		if !profile.Crop.Disabled {
			notes = append(notes, "Crop: detected when the job runs, not shown in the command")
		}
	}
	return command, notes, nil
}

// replaceQuality swaps the value of every quality option of the video encoder
// for the placeholder. Which option carries it depends on the encoder family;
// see pkg/ffmpeg.
func replaceQuality(command []string) {
	for index := 0; index+1 < len(command); index++ {
		switch command[index] {
		case "-crf", "-global_quality", "-qp_i", "-qp_p", "-qp_b":
			command[index+1] = crfPlaceholder
			index++
		}
	}
}
