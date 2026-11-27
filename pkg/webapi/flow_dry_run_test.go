package webapi

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/flow"
)

func TestFlowDryRun(t *testing.T) {
	f := newFixture(t, "", nil)
	film := f.writeMedia("film.mkv")
	f.mediaLibrary("movies", f.defaultFlow().ID)
	test := func(graph flow.Graph, path string) FlowTest {
		t.Helper()
		return want[FlowTest](t, f.send(http.MethodPost, "/api/v1/flows/test", FlowTestRequest{Graph: graph, Path: path}), http.StatusOK)
	}

	// A fixed CRF is in the command as it is, and the output is a placeholder.
	fixed := test(skipHEVC(), film)
	if fixed.Outcome != flow.OutcomeProcess || !fixed.Check.Valid ||
		!slices.Equal(fixed.Path, []string{"in", "is-hevc", "encode", "audio", "publish"}) || len(fixed.Decisions) != 1 {
		t.Fatalf("dry run = %+v", fixed)
	}
	if fixed.File.Path != film || fixed.File.VideoCodec != "h264" || fixed.File.Container != "mkv" || fixed.File.Height != 1080 ||
		len(fixed.File.Audio) != 1 || fixed.File.Audio[0].Channels != 2 || !fixed.File.Audio[0].Default || len(fixed.File.Subtitles) != 1 {
		t.Fatalf("file facts = %+v", fixed.File)
	}
	if value := after(fixed.Command, "-crf"); value != "30" {
		t.Fatalf("command = %q, want -crf 30", fixed.Command)
	}
	if fixed.Command[len(fixed.Command)-1] != outputPlaceholder || after(fixed.Command, "-i") != film {
		t.Fatalf("command = %q, want the input and the {output} placeholder", fixed.Command)
	}
	plan := strings.Join(fixed.Plan, "\n")
	// The flow keeps the original-language audio, and a dry run looks no
	// language up: it has to say that it kept everything for that reason.
	if !strings.Contains(plan, "Original language") || strings.Contains(plan, "CRF:") {
		t.Fatalf("plan = %q", fixed.Plan)
	}

	// A quality search has yet to pick the CRF, and the crop is detected at
	// run time: the command says so with a placeholder, the plan in words.
	searched := test(flow.Default(), film)
	if searched.Outcome != flow.OutcomeProcess || after(searched.Command, "-crf") != crfPlaceholder {
		t.Fatalf("dry run of a search = %+v, want -crf {crf}", searched.Command)
	}
	plan = strings.Join(searched.Plan, "\n")
	if !strings.Contains(plan, "CRF:") || !strings.Contains(plan, "Crop:") || strings.Contains(strings.Join(searched.Command, " "), "crop=") {
		t.Fatalf("plan = %q, command = %q", searched.Plan, searched.Command)
	}

	// A skipped file has a plan and no command.
	f.codec = "hevc"
	skipped := test(skipHEVC(), film)
	if skipped.Outcome != flow.OutcomeSkip || skipped.Command == nil || len(skipped.Command) != 0 || len(skipped.Plan) != 1 ||
		!slices.Equal(skipped.Path, []string{"in", "is-hevc", "skip"}) {
		t.Fatalf("dry run of a skip = %+v", skipped)
	}

	// A graph with errors is checked, not evaluated.
	broken := test(flow.Graph{Nodes: []flow.Node{{ID: "in", Type: "input"}}}, film)
	if broken.Check.Valid || len(broken.Check.Issues) == 0 || broken.Outcome != "" || broken.Path == nil || broken.Command == nil || broken.File.Audio == nil {
		t.Fatalf("dry run of a broken graph = %+v", broken)
	}
}

func TestFlowDryRunOnlyReadsLibraryFiles(t *testing.T) {
	f := newFixture(t, "", nil)
	f.writeMedia("film.mkv")
	f.mediaLibrary("movies", f.defaultFlow().ID)
	outside := filepath.Join(t.TempDir(), "secret.mkv")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.media, "link.mkv")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"no path":                  "",
		"a relative path":          "film.mkv",
		"a file outside":           outside,
		"a link that leads out":    link,
		"a path that climbs out":   f.media + "/../" + filepath.Base(filepath.Dir(outside)) + "/secret.mkv",
		"a missing file":           filepath.Join(f.media, "gone.mkv"),
		"the library root":         f.media,
		"a directory in a library": filepath.Dir(f.writeMedia("show/episode.mkv")),
	} {
		refused := wantError(t, f.send(http.MethodPost, "/api/v1/flows/test", FlowTestRequest{Graph: skipHEVC(), Path: path}), http.StatusBadRequest, CodeInvalid)
		if refused.Field != "path" {
			t.Errorf("%s: %+v, want the path field named", name, refused)
		}
	}

	// What lies outside the libraries gets one answer, whether it exists or not.
	message := func(path string) string {
		refused := wantError(t, f.send(http.MethodPost, "/api/v1/flows/test", FlowTestRequest{Graph: skipHEVC(), Path: path}), http.StatusBadRequest, CodeInvalid)
		return strings.ReplaceAll(refused.Message, path, "PATH")
	}
	if there, missing := message(outside), message(outside+".gone"); there != missing || !strings.Contains(there, "not inside a library") {
		t.Errorf("an existing file outside answers %q, a missing one %q", there, missing)
	}
}

// after returns the argument that follows a flag, or "" without the flag.
func after(command []string, flag string) string {
	index := slices.Index(command, flag)
	if index < 0 || index+1 >= len(command) {
		return ""
	}
	return command[index+1]
}
