package flow

import (
	"reflect"
	"slices"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestOutputsSetThePublishPolicy(t *testing.T) {
	tests := []struct {
		name     string
		node     Node
		want     domain.PublishPolicy
		wantPlan string
	}{
		{
			name:     "replace",
			node:     newNode("out", typeOutputReplace, nil),
			want:     domain.PublishPolicy{Mode: domain.PublishReplace},
			wantPlan: "Output: replace the source in place",
		},
		{
			name:     "move at its defaults",
			node:     newNode("out", typeOutputMove, map[string]any{"destination": "/srv/imports/tv/"}),
			want:     domain.PublishPolicy{Mode: domain.PublishMove, Destination: "/srv/imports/tv", PreserveStructure: true},
			wantPlan: "Output: move to /srv/imports/tv; delete the source",
		},
		{
			name:     "move that prunes",
			node:     newNode("out", typeOutputMove, map[string]any{"destination": "/srv/imports/tv", "prune_empty_dirs": true}),
			want:     domain.PublishPolicy{Mode: domain.PublishMove, Destination: "/srv/imports/tv", PreserveStructure: true, PruneEmptyDirs: true},
			wantPlan: "Output: move to /srv/imports/tv; delete the source and the folders it leaves empty",
		},
		{
			// prune_empty_dirs is hidden while the source stays, so a stored
			// true has no effect.
			name:     "move that keeps the source",
			node:     newNode("out", typeOutputMove, map[string]any{"destination": "/srv/imports/tv", "keep_source": true, "prune_empty_dirs": true, "preserve_structure": false}),
			want:     domain.PublishPolicy{Mode: domain.PublishMove, Destination: "/srv/imports/tv", KeepSource: true},
			wantPlan: "Output: move to /srv/imports/tv, without the library's folders; keep the source",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := viaJSON(t, chain(newNode("in", typeInput, nil), newNode("copy", typeVideoCopy, nil), test.node))
			resolution := mustEvaluate(t, g, probeOf(videoStream("hevc", 1920, 1080)))
			if resolution.Outcome != OutcomeProcess {
				t.Fatalf("Outcome = %q, want %q", resolution.Outcome, OutcomeProcess)
			}
			if !reflect.DeepEqual(resolution.Profile.Publish, test.want) {
				t.Errorf("Publish = %+v, want %+v", resolution.Profile.Publish, test.want)
			}
			if !resolution.Profile.Video.SkipEncode {
				t.Errorf("the output undid the video copy: %+v", resolution.Profile.Video)
			}
			if got := resolution.Plan[len(resolution.Plan)-1]; got != test.wantPlan {
				t.Errorf("Plan ends with %q, want %q", got, test.wantPlan)
			}
		})
	}
}

func TestMoveDestinationMustBeAnAbsoluteFolder(t *testing.T) {
	tests := map[string]struct {
		destination any
		want        string
	}{
		"missing":  {"", "Enter the folder to move the result to, as an absolute path such as /srv/imports/tv."},
		"blank":    {"   ", "Enter the folder to move the result to, as an absolute path such as /srv/imports/tv."},
		"relative": {"imports/tv", `"imports/tv" is not an absolute path. Enter one that starts with /, such as /srv/imports/tv.`},
		"root":     {"//", "The folder cannot be the filesystem root. Enter a folder such as /srv/imports/tv."},
		"not text": {42.0, "Must be a folder path."},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			g := viaJSON(t, chain(newNode("in", typeInput, nil), newNode("out", typeOutputMove, map[string]any{"destination": test.destination})))
			want := []Issue{problem("out", "destination", test.want)}
			if got := errorIssues(Validate(g)); !reflect.DeepEqual(got, want) {
				t.Errorf("Validate() =\n%s\nwant\n%s", printIssues(got), printIssues(want))
			}
		})
	}
}

func TestCanonicalMoveDestination(t *testing.T) {
	move := func(options map[string]any) []byte {
		t.Helper()
		data, err := Canonical(viaJSON(t, chain(newNode("in", typeInput, nil), newNode("out", typeOutputMove, options))))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	base := move(map[string]any{"destination": "/srv/imports/tv"})
	if spelled := move(map[string]any{"destination": "/srv/imports/tv/", "keep_source": false, "preserve_structure": true}); string(spelled) != string(base) {
		t.Errorf("a trailing slash or a spelled-out default changed the canonical form:\n%s\nwant\n%s", spelled, base)
	}
	for _, options := range []map[string]any{
		{"destination": "/srv/imports/movies"},
		{"destination": "/srv/imports/tv", "keep_source": true},
		{"destination": "/srv/imports/tv", "preserve_structure": false},
		{"destination": "/srv/imports/tv", "prune_empty_dirs": true},
	} {
		if changed := move(options); string(changed) == string(base) {
			t.Errorf("%v has the same canonical form as the default move", options)
		}
	}
}

func TestMoveTargetsAndPublishPolicies(t *testing.T) {
	g := viaJSON(t, Graph{
		Nodes: []Node{
			newNode("in", typeInput, nil),
			newNode("check", typeCheckVideoCodec, map[string]any{"codecs": []string{"hevc"}}),
			newNode("tv", typeOutputMove, map[string]any{"destination": "/srv/imports/tv/"}),
			newNode("again", typeOutputMove, map[string]any{"destination": "/srv/imports/tv", "keep_source": true}),
			newNode("unset", typeOutputMove, nil),
			newNode("replace", typeOutputReplace, nil),
			newNode("skip", typeOutputSkip, nil),
		},
	})
	wantTargets := []MoveTarget{{NodeID: "tv", Folder: "/srv/imports/tv"}, {NodeID: "again", Folder: "/srv/imports/tv"}}
	if got := MoveTargets(g); !slices.Equal(got, wantTargets) {
		t.Errorf("MoveTargets() = %+v, want %+v", got, wantTargets)
	}
	if got, want := MoveDestinations(g), []string{"/srv/imports/tv"}; !slices.Equal(got, want) {
		t.Errorf("MoveDestinations() = %q, want %q", got, want)
	}
	wantPolicies := []domain.PublishPolicy{
		{Mode: domain.PublishMove, Destination: "/srv/imports/tv", PreserveStructure: true},
		{Mode: domain.PublishMove, Destination: "/srv/imports/tv", PreserveStructure: true, KeepSource: true},
		{Mode: domain.PublishMove, PreserveStructure: true},
		{Mode: domain.PublishReplace},
	}
	if got := PublishPolicies(g); !slices.Equal(got, wantPolicies) {
		t.Errorf("PublishPolicies() = %+v, want %+v", got, wantPolicies)
	}
}

func TestCropBeforeAMoveWarnsLikeBeforeAReplace(t *testing.T) {
	for _, output := range []Node{newNode("out", typeOutputReplace, nil), newNode("out", typeOutputMove, map[string]any{"destination": "/srv/imports"})} {
		g := viaJSON(t, chain(newNode("in", typeInput, nil), newNode("copy", typeVideoCopy, nil), newNode("crop", typeVideoCrop, nil), output))
		warned := slices.ContainsFunc(Validate(g), func(issue Issue) bool {
			return issue.Severity == SeverityWarning && issue.NodeID == "crop"
		})
		if !warned {
			t.Errorf("%s: no warning that a copied video is not cropped:\n%s", output.Type, printIssues(Validate(g)))
		}
	}
}
