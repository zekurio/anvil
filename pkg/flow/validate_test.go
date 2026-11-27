package flow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func problem(nodeID, field, message string) Issue {
	return Issue{Severity: SeverityError, NodeID: nodeID, Field: field, Message: message}
}

func warning(nodeID, message string) Issue {
	return Issue{Severity: SeverityWarning, NodeID: nodeID, Message: message}
}

// simple is the smallest valid flow around one node.
func simple(node Node) Graph {
	return chain(newNode("in", typeInput, nil), node, newNode("out", typeOutputPublish, nil))
}

func TestValidateAcceptsAValidGraph(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			newNode("in", typeInput, nil),
			{ID: "check", Type: typeCheckVideoCodec, Label: "Video is hevc?", Options: map[string]any{"codecs": []string{"hevc"}}},
			newNode("copy", typeVideoCopy, nil),
			newNode("encode", typeVideoEncode, nil),
			newNode("audio", typeAudioKeep, nil),
			newNode("out", typeOutputPublish, nil),
		},
		Edges: []Edge{
			{From: "in", To: "check"},
			{From: "check", Port: 0, To: "copy"},
			{From: "check", Port: 1, To: "encode"},
			// Both branches rejoin: a node may have several incoming edges.
			{From: "copy", To: "audio"},
			{From: "encode", To: "audio"},
			{From: "audio", To: "out"},
		},
	}
	issues := Validate(viaJSON(t, g))
	if issues == nil || len(issues) != 0 {
		t.Fatalf("Validate() = %#v, want an empty, non-nil list", issues)
	}
}

func TestValidateStructure(t *testing.T) {
	check := Node{ID: "check", Type: typeCheckVideoCodec, Label: "Video is hevc?"}
	tests := []struct {
		name  string
		graph Graph
		want  []Issue
	}{
		{
			name:  "no input node",
			graph: Graph{Nodes: []Node{newNode("out", typeOutputPublish, nil)}},
			want:  []Issue{problem("", "", "A flow needs an input node. Add one.")},
		},
		{
			name: "two input nodes",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("again", typeInput, nil), newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{{From: "in", To: "out"}, {From: "again", To: "out"}},
			},
			want: []Issue{
				problem("again", "", "A flow has exactly one input node. Remove this one."),
				warning("again", `"Input file" is not connected to the input, so no file reaches it.`),
			},
		},
		{
			name: "node without an ID",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("", typeVideoCopy, nil), newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{{From: "in", To: "out"}},
			},
			want: []Issue{problem("", "", "Every node needs an ID.")},
		},
		{
			name: "duplicate node ID",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil), newNode("out", typeOutputSkip, nil)},
				Edges: []Edge{{From: "in", To: "out"}},
			},
			want: []Issue{problem("out", "", `Node ID "out" is used more than once. Every node needs its own ID.`)},
		},
		{
			name:  "unknown node type",
			graph: simple(newNode("mystery", "video.upscale", nil)),
			want: []Issue{
				problem("mystery", "", `"video.upscale" is not a node type Anvil knows. Replace this node.`),
			},
		},
		{
			name: "edge from a missing node",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{{From: "in", To: "out"}, {From: "ghost", To: "out"}},
			},
			want: []Issue{problem("", "", `An edge starts at "ghost", which is not a node of this flow. Remove the edge.`)},
		},
		{
			name: "edge to a missing node",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{{From: "in", To: "ghost"}},
			},
			want: []Issue{
				problem("in", "", `An edge from "Input file" leads to "ghost", which is not a node of this flow. Remove the edge.`),
				problem("in", "", `Output of "Input file" is not connected.`),
				warning("out", `"Publish result" is not connected to the input, so no file reaches it.`),
			},
		},
		{
			name: "port index out of range",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), check, newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{
					{From: "in", To: "check"},
					{From: "check", Port: 0, To: "out"},
					{From: "check", Port: 1, To: "out"},
					{From: "check", Port: 2, To: "out"},
				},
			},
			want: []Issue{problem("check", "", `"Video is hevc?" has outputs 0 to 1, and an edge uses output 2. Remove the edge.`)},
		},
		{
			name: "negative port index",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{{From: "in", To: "out"}, {From: "in", Port: -1, To: "out"}},
			},
			want: []Issue{problem("in", "", `"Input file" has outputs 0 to 0, and an edge uses output -1. Remove the edge.`)},
		},
		{
			name: "edge leaving an output node",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil), newNode("skip", typeOutputSkip, nil)},
				Edges: []Edge{{From: "in", To: "out"}, {From: "out", To: "skip"}},
			},
			want: []Issue{
				problem("out", "", `"Publish result" ends the flow and has no outputs. Remove the edge that leaves it.`),
				warning("skip", `"Skip file" is not connected to the input, so no file reaches it.`),
			},
		},
		{
			name: "two edges from one output",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), check, newNode("out", typeOutputPublish, nil), newNode("skip", typeOutputSkip, nil)},
				Edges: []Edge{
					{From: "in", To: "check"},
					{From: "check", Port: 0, To: "out"},
					{From: "check", Port: 1, To: "skip"},
					{From: "check", Port: 1, To: "out"},
				},
			},
			want: []Issue{problem("check", "", `"false" output of "Video is hevc?" has more than one edge. An output leads to exactly one node.`)},
		},
		{
			name: "edge into the input node",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("copy", typeVideoCopy, nil)},
				Edges: []Edge{{From: "in", To: "copy"}, {From: "copy", To: "in"}},
			},
			want: []Issue{
				problem("in", "", `The input node starts the flow, so no edge can lead into it. Remove the edge from "Copy video".`),
				problem("copy", "", `Output of "Copy video" is not connected.`),
			},
		},
		{
			name: "cycle",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), check, newNode("copy", typeVideoCopy, nil), newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{
					{From: "in", To: "check"},
					{From: "check", Port: 0, To: "copy"},
					{From: "check", Port: 1, To: "out"},
					{From: "copy", To: "check"},
				},
			},
			want: []Issue{problem("check", "", `The flow loops back to "Video is hevc?". Every path has to end at an output without repeating a node.`)},
		},
		{
			name:  "node looping to itself",
			graph: Graph{Nodes: []Node{newNode("in", typeInput, nil), newNode("copy", typeVideoCopy, nil)}, Edges: []Edge{{From: "in", To: "copy"}, {From: "copy", To: "copy"}}},
			want:  []Issue{problem("copy", "", `The flow loops back to "Copy video". Every path has to end at an output without repeating a node.`)},
		},
		{
			name: "dangling labeled output",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), check, newNode("out", typeOutputPublish, nil)},
				Edges: []Edge{{From: "in", To: "check"}, {From: "check", Port: 0, To: "out"}},
			},
			want: []Issue{problem("check", "", `"false" output of "Video is hevc?" is not connected.`)},
		},
		{
			name:  "dangling unlabeled output",
			graph: chain(newNode("in", typeInput, nil), newNode("encode", typeVideoEncode, nil)),
			want:  []Issue{problem("encode", "", `Output of "Encode video" is not connected.`)},
		},
		{
			name: "unreachable nodes only warn, dangling or not",
			graph: Graph{
				Nodes: []Node{newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil), newNode("spare", typeAudioKeep, nil), newNode("skip", typeOutputSkip, nil)},
				Edges: []Edge{{From: "in", To: "out"}},
			},
			want: []Issue{
				warning("spare", `"Keep audio tracks" is not connected to the input, so no file reaches it.`),
				warning("skip", `"Skip file" is not connected to the input, so no file reaches it.`),
			},
		},
		{
			name: "crop on a path that copies the video",
			graph: Graph{
				Nodes: []Node{
					newNode("in", typeInput, nil),
					newNode("crop", typeVideoCrop, nil),
					check,
					newNode("copy", typeVideoCopy, nil),
					newNode("encode", typeVideoEncode, nil),
					newNode("out", typeOutputPublish, nil),
				},
				Edges: []Edge{
					{From: "in", To: "crop"},
					{From: "crop", To: "check"},
					{From: "check", Port: 0, To: "copy"},
					{From: "check", Port: 1, To: "encode"},
					{From: "copy", To: "out"},
					{From: "encode", To: "out"},
				},
			},
			want: []Issue{warning("crop", `Cropping needs a re-encode, but a path through "Crop black bars" copies the video, so nothing is cropped there.`)},
		},
		{
			name:  "crop without any video node",
			graph: simple(newNode("crop", typeVideoCrop, nil)),
			want:  []Issue{warning("crop", `Cropping needs a re-encode, but a path through "Crop black bars" copies the video, so nothing is cropped there.`)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Validate(test.graph)
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("Validate() =\n%s\nwant\n%s", printIssues(got), printIssues(test.want))
			}
			if again := Validate(test.graph); !reflect.DeepEqual(again, got) {
				t.Errorf("Validate() is not deterministic:\n%s\nthen\n%s", printIssues(got), printIssues(again))
			}
		})
	}
}

func TestValidateCropIsFineWhereEveryPathEncodes(t *testing.T) {
	g := chain(
		newNode("in", typeInput, nil),
		newNode("copy", typeVideoCopy, nil),
		newNode("crop", typeVideoCrop, nil),
		newNode("encode", typeVideoEncode, nil),
		newNode("out", typeOutputPublish, nil),
	)
	if issues := Validate(g); len(issues) != 0 {
		t.Fatalf("Validate() = %s", printIssues(issues))
	}
	// A path that ends in a skip crops nothing either way.
	g = chain(newNode("in", typeInput, nil), newNode("crop", typeVideoCrop, nil), newNode("out", typeOutputSkip, nil))
	if issues := Validate(g); len(issues) != 0 {
		t.Fatalf("Validate() = %s", printIssues(issues))
	}
}

func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name     string
		nodeType string
		options  string
		want     []Issue
	}{
		{
			name:     "unknown option key",
			nodeType: typeVideoCopy,
			options:  `{"speed": "fast"}`,
			want:     []Issue{problem("n", "speed", `"speed" is not an option of this node. Remove it.`)},
		},
		{
			name:     "string field",
			nodeType: typeVideoEncode,
			options:  `{"preset": 6}`,
			want:     []Issue{problem("n", "preset", "Must be text.")},
		},
		{
			name:     "int field given text",
			nodeType: typeVideoEncode,
			options:  `{"crf_min": "18"}`,
			want:     []Issue{problem("n", "crf_min", "Must be a whole number.")},
		},
		{
			name:     "int field given a fraction",
			nodeType: typeVideoEncode,
			options:  `{"crf_min": 18.5}`,
			want:     []Issue{problem("n", "crf_min", "Must be a whole number.")},
		},
		{
			name:     "int field out of range",
			nodeType: typeVideoEncode,
			options:  `{"quality_mode": "fixed", "crf": 300}`,
			want:     []Issue{problem("n", "crf", "Must be between 0 and 255.")},
		},
		{
			name:     "int field below its only bound",
			nodeType: typeCheckBitrate,
			options:  `{"kbps": 0}`,
			want:     []Issue{problem("n", "kbps", "Must be at least 1.")},
		},
		{
			name:     "float field",
			nodeType: typeVideoEncode,
			options:  `{"target": true}`,
			want:     []Issue{problem("n", "target", "Must be a number.")},
		},
		{
			name:     "float field out of range",
			nodeType: typeVideoEncode,
			options:  `{"target": 100.5}`,
			want:     []Issue{problem("n", "target", "Must be between 0 and 100.")},
		},
		{
			name:     "bool field",
			nodeType: typeAudioKeep,
			options:  `{"keep_commentary": "yes"}`,
			want:     []Issue{problem("n", "keep_commentary", "Must be true or false.")},
		},
		{
			name:     "enum field",
			nodeType: typeVideoEncode,
			options:  `{"codec": "vp9"}`,
			want:     []Issue{problem("n", "codec", "Must be one of: AV1, HEVC (H.265), H.264.")},
		},
		{
			name:     "enum field given a number",
			nodeType: typeVideoEncode,
			options:  `{"bit_depth": 10}`,
			want:     []Issue{problem("n", "bit_depth", "Must be one of: 10-bit, 8-bit.")},
		},
		{
			name:     "enum list field given one value",
			nodeType: typeCheckHDR,
			options:  `{"kinds": "hdr10"}`,
			want:     []Issue{problem("n", "kinds", "Must be a list of: HDR10, HLG, Dolby Vision.")},
		},
		{
			name:     "enum list field with an unknown entry",
			nodeType: typeCheckHDR,
			options:  `{"kinds": ["hdr10", "hdr10+"]}`,
			want:     []Issue{problem("n", "kinds", "Must be a list of: HDR10, HLG, Dolby Vision.")},
		},
		{
			name:     "string list field",
			nodeType: typeAudioKeep,
			options:  `{"languages": ["eng", 7]}`,
			want:     []Issue{problem("n", "languages", "Must be a list of text values.")},
		},
		{
			name:     "duration field",
			nodeType: typeVideoEncode,
			options:  `{"sample_duration": "twenty seconds"}`,
			want:     []Issue{problem("n", "sample_duration", `Must be a duration such as "20s".`)},
		},
		{
			name:     "null is not a value",
			nodeType: typeVideoEncode,
			options:  `{"preset": null}`,
			want:     []Issue{problem("n", "preset", "Must be text.")},
		},
		{
			name:     "empty required list",
			nodeType: typeCheckVideoCodec,
			options:  `{"codecs": []}`,
			want:     []Issue{problem("n", "codecs", "Add at least one codec.")},
		},
		{
			name:     "empty HDR list",
			nodeType: typeCheckHDR,
			options:  `{"kinds": []}`,
			want:     []Issue{problem("n", "kinds", "Choose at least one HDR format.")},
		},
		{
			name:     "empty language condition",
			nodeType: typeCheckAudioLanguage,
			options:  `{"languages": []}`,
			want:     []Issue{problem("n", "languages", "Add at least one language.")},
		},
		{
			name:     "empty audio codec condition",
			nodeType: typeCheckAudioCodec,
			options:  `{"codecs": []}`,
			want:     []Issue{problem("n", "codecs", "Add at least one codec.")},
		},
		{
			name:     "blank list entry",
			nodeType: typeSubtitlesKeep,
			options:  `{"languages": ["eng", " "]}`,
			want:     []Issue{problem("n", "languages", "Entries must not be empty. Remove the blank one.")},
		},
		{
			name:     "blank source codec",
			nodeType: typeAudioEncode,
			options:  `{"source_codecs": [""]}`,
			want:     []Issue{problem("n", "source_codecs", "Entries must not be empty. Remove the blank one.")},
		},
		{
			name:     "config rule: CRF range is ordered",
			nodeType: typeVideoEncode,
			options:  `{"crf_min": 41}`,
			// The rule comes from the TOML profile and names the other option
			// by its key; the form shows labels, so the message does too.
			want: []Issue{problem("n", "crf_min", "Must be less than or equal to Highest CRF.")},
		},
		{
			name:     "config rule: managed ffmpeg option",
			nodeType: typeVideoEncode,
			options:  `{"ffmpeg_args": ["-crf", "20"]}`,
			want:     []Issue{problem("n", "ffmpeg_args", `Option "-crf" is not accepted; accepted: encoder parameter bundles (-svtav1-params, -x265-params, -x264-params) or value-only tuning options (-tune, -profile:v, -level, -g, -keyint_min, -bf, -refs, -sc_threshold, -aq-mode, -aq-strength, -rc-lookahead, -b_strategy, -look_ahead, -look_ahead_depth, -extbrc, -low_power, -async_depth, -adaptive_i, -adaptive_b, -quality).`)},
		},
		{
			name:     "config rule: managed encoder param",
			nodeType: typeVideoEncode,
			options:  `{"ffmpeg_args": ["-svtav1-params", "tune=0:crf=20"]}`,
			want:     []Issue{problem("n", "ffmpeg_args", `Option "-svtav1-params": encoder param "crf" overrides a setting managed by Anvil.`)},
		},
		{
			name:     "config rule: ffmpeg options come in pairs",
			nodeType: typeVideoEncode,
			options:  `{"ffmpeg_args": ["-g"]}`,
			want:     []Issue{problem("n", "ffmpeg_args", `Must contain option/value pairs; invalid option "-g"; accepted: encoder parameter bundles (-svtav1-params, -x265-params, -x264-params) or value-only tuning options (-tune, -profile:v, -level, -g, -keyint_min, -bf, -refs, -sc_threshold, -aq-mode, -aq-strength, -rc-lookahead, -b_strategy, -look_ahead, -look_ahead_depth, -extbrc, -low_power, -async_depth, -adaptive_i, -adaptive_b, -quality).`)},
		},
		{
			name:     "config rule: xpsnr needs a target",
			nodeType: typeVideoEncode,
			options:  `{"metric": "xpsnr", "target": 0}`,
			want:     []Issue{problem("n", "target", `Must be set when metric is "xpsnr" (typical targets are 35-50).`)},
		},
		{
			name:     "fixed CRF outside what the encoder accepts",
			nodeType: typeVideoEncode,
			options:  `{"codec": "hevc", "quality_mode": "fixed", "crf": 60}`,
			want:     []Issue{problem("n", "crf", "Must be between 0 and 51 for libx265.")},
		},
		{
			name:     "fixed CRF of zero reads as unset in libsvtav1",
			nodeType: typeVideoEncode,
			options:  `{"quality_mode": "fixed", "crf": 0}`,
			want:     []Issue{problem("n", "crf", "Must be between 1 and 63 for libsvtav1.")},
		},
		{
			name:     "search range outside what the encoder accepts",
			nodeType: typeVideoEncode,
			options:  `{"codec": "h264", "accelerator": "qsv", "crf_min": 0, "crf_max": 60}`,
			want: []Issue{
				problem("n", "crf_min", "Must be between 1 and 51 for h264_qsv."),
				problem("n", "crf_max", "Must be between 1 and 51 for h264_qsv."),
			},
		},
		{
			name:     "hidden search range is not checked in fixed mode",
			nodeType: typeVideoEncode,
			options:  `{"codec": "hevc", "quality_mode": "fixed", "crf": 22, "crf_max": 200}`,
		},
		{
			name:     "xpsnr target no encode reaches",
			nodeType: typeVideoEncode,
			options:  `{"metric": "xpsnr", "target": 95}`,
			want: []Issue{{
				Severity: SeverityWarning, NodeID: "n", Field: "target",
				Message: "An XPSNR target of 95 dB is higher than encodes reach, so no CRF will meet it. Typical targets are 35 to 50.",
			}},
		},
		{
			name:     "plausible xpsnr target",
			nodeType: typeVideoEncode,
			options:  `{"metric": "xpsnr", "target": 42}`,
		},
		{
			name:     "config rule: sample length is positive",
			nodeType: typeVideoEncode,
			options:  `{"sample_duration": "0s"}`,
			want:     []Issue{problem("n", "sample_duration", "Must be positive.")},
		},
		{
			name:     "config rule: crop samples",
			nodeType: typeVideoCrop,
			options:  `{"samples": 1}`,
			want:     []Issue{problem("n", "samples", "Must be 0 (automatic) or at least 2.")},
		},
		{
			// The field's own range says so before the rule gets a say: the
			// catalog must not advertise a minimum the rule then refuses.
			name:     "retained area is above zero",
			nodeType: typeVideoCrop,
			options:  `{"min_retained_area_percent": 0}`,
			want:     []Issue{problem("n", "min_retained_area_percent", "Must be greater than 0 and at most 100.")},
		},
		{
			name:     "retained area just above zero",
			nodeType: typeVideoCrop,
			options:  `{"min_retained_area_percent": 0.5}`,
		},
		{
			name:     "retained area above the whole picture",
			nodeType: typeVideoCrop,
			options:  `{"min_retained_area_percent": 101}`,
			want:     []Issue{problem("n", "min_retained_area_percent", "Must be greater than 0 and at most 100.")},
		},
		{
			name:     "config rule: seek offsets are not negative",
			nodeType: typeVideoCrop,
			options:  `{"seek_offsets": ["5m", "-1m", "-2m"]}`,
			want:     []Issue{problem("n", "seek_offsets", "Must not contain negative durations.")},
		},
		{
			name:     "seek offsets are durations",
			nodeType: typeVideoCrop,
			options:  `{"seek_offsets": ["5m", "later"]}`,
			want:     []Issue{problem("n", "seek_offsets", `Entries must be durations such as 5m or 1h10m. "later" is not one.`)},
		},
		{
			name:     "crop limit range",
			nodeType: typeVideoCrop,
			options:  `{"limit": 256}`,
			want:     []Issue{problem("n", "limit", "Must be between 1 and 255.")},
		},
		{
			name:     "a field that failed to decode is not judged again",
			nodeType: typeVideoEncode,
			options:  `{"crf_min": 18, "crf_max": "40", "target": -1}`,
			want: []Issue{
				problem("n", "target", "Must be between 0 and 100."),
				problem("n", "crf_max", "Must be a whole number."),
			},
		},
		{
			// crf_min falls back to 18, above this crf_max, but the rule
			// about their order would only be judging that stand-in.
			name:     "a rule does not judge the default standing in for a bad value",
			nodeType: typeVideoEncode,
			options:  `{"crf_min": "low", "crf_max": 10}`,
			want:     []Issue{problem("n", "crf_min", "Must be a whole number.")},
		},
		{
			name:     "several problems come in field order, unknown keys last",
			nodeType: typeVideoEncode,
			options:  `{"zzz": 1, "aaa": 1, "ffmpeg_args": ["-map", "0"], "codec": "theora"}`,
			want: []Issue{
				problem("n", "codec", "Must be one of: AV1, HEVC (H.265), H.264."),
				problem("n", "ffmpeg_args", `Option "-map" is not accepted; accepted: encoder parameter bundles (-svtav1-params, -x265-params, -x264-params) or value-only tuning options (-tune, -profile:v, -level, -g, -keyint_min, -bf, -refs, -sc_threshold, -aq-mode, -aq-strength, -rc-lookahead, -b_strategy, -look_ahead, -look_ahead_depth, -extbrc, -low_power, -async_depth, -adaptive_i, -adaptive_b, -quality).`),
				problem("n", "aaa", `"aaa" is not an option of this node. Remove it.`),
				problem("n", "zzz", `"zzz" is not an option of this node. Remove it.`),
			},
		},
		{
			name:     "hidden fields are not checked",
			nodeType: typeVideoEncode,
			options:  `{"quality_mode": "fixed", "crf": 22, "crf_min": "garbage", "crf_max": -4, "target": [], "sample_duration": 3}`,
		},
		{
			name:     "the hidden CRF is not checked in search mode",
			nodeType: typeVideoEncode,
			options:  `{"crf": "garbage"}`,
		},
		{
			name:     "the bitrate is hidden for flac",
			nodeType: typeAudioEncode,
			options:  `{"codec": "flac", "bitrate_kbps_per_channel": -1}`,
		},
		{
			name:     "the bitrate is checked for other codecs",
			nodeType: typeAudioDownmix,
			options:  `{"codec": "aac", "bitrate_kbps_per_channel": -1}`,
			want:     []Issue{problem("n", "bitrate_kbps_per_channel", "Must be between 0 and 512.")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var values map[string]any
			if err := json.Unmarshal([]byte(test.options), &values); err != nil {
				t.Fatal(err)
			}
			// Keep the graph valid apart from the node under test.
			g := chain(newNode("in", typeInput, nil), newNode("n", test.nodeType, values), newNode("encode", typeVideoEncode, nil), newNode("out", typeOutputPublish, nil))
			if nodeDefsByID[test.nodeType].Kind == KindCondition {
				g.Edges = append(g.Edges, Edge{From: "n", Port: 1, To: "out"})
			}
			got := Validate(g)
			if len(got) != len(test.want) || (len(test.want) > 0 && !reflect.DeepEqual(got, test.want)) {
				t.Errorf("Validate() =\n%s\nwant\n%s", printIssues(got), printIssues(test.want))
			}
		})
	}
}

func TestValidateMessagesFollowTheCopyRules(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			newNode("in", typeInput, nil),
			newNode("again", typeInput, nil),
			newNode("", typeVideoCopy, nil),
			newNode("mystery", "video.upscale", nil),
			newNode("check", typeCheckVideoCodec, map[string]any{"codecs": []any{}, "extra": 1}),
			newNode("encode", typeVideoEncode, map[string]any{"crf_min": 99, "preset": 1, "target": 400, "ffmpeg_args": []any{"-crf", "1"}}),
			newNode("out", typeOutputPublish, nil),
			newNode("spare", typeOutputSkip, nil),
		},
		Edges: []Edge{
			{From: "in", To: "check"},
			{From: "check", Port: 0, To: "encode"},
			{From: "check", Port: 0, To: "out"},
			{From: "check", Port: 5, To: "out"},
			{From: "encode", To: "check"},
			{From: "out", To: "in"},
			{From: "ghost", To: "nowhere"},
			{From: "in", To: "nowhere"},
		},
	}
	issues := Validate(g)
	if len(issues) < 12 {
		t.Fatalf("expected a broad sample of issues, got:\n%s", printIssues(issues))
	}
	for _, issue := range issues {
		message := issue.Message
		first := message[:1]
		// A message may open with a quoted name; otherwise sentence case.
		if first != `"` && first != strings.ToUpper(first) {
			t.Errorf("message does not start in sentence case: %q", message)
		}
		if !strings.HasSuffix(message, ".") {
			t.Errorf("message does not end with a period: %q", message)
		}
		if strings.Contains(message, "!") || strings.Contains(strings.ToLower(message), "invalid value") {
			t.Errorf("message breaks the copy rules: %q", message)
		}
	}
}

func printIssues(issues []Issue) string {
	if len(issues) == 0 {
		return "  (none)"
	}
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		lines = append(lines, "  "+string(issue.Severity)+" node="+issue.NodeID+" field="+issue.Field+": "+issue.Message)
	}
	return strings.Join(lines, "\n")
}

// A message about one option that mentions another must use the words the
// form shows. No option key of any node type may survive in one.
func TestMessagesNameFieldsByLabel(t *testing.T) {
	for _, def := range nodeDefs() {
		for _, section := range def.Sections {
			for _, field := range section.Fields {
				if !strings.Contains(field.Key, "_") {
					continue
				}
				got := nameFields(def, "must be less than or equal to "+field.Key+", and "+field.Key+"_other stays")
				want := "must be less than or equal to " + field.Label + ", and " + field.Key + "_other stays"
				if got != want {
					t.Errorf("%s.%s: nameFields = %q, want %q", def.ID, field.Key, got, want)
				}
			}
		}
	}
}
