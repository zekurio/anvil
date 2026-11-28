package ffmpeg

import (
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSampleAndFinalVideoSettingsMatch(t *testing.T) {
	for _, encoder := range []string{"libsvtav1", "hevc_qsv", "av1_vaapi", "h264_amf"} {
		t.Run(encoder, func(t *testing.T) {
			plan := domain.EncodePlan{InputPath: "input.mkv", OutputPath: "output.mkv", VideoCodec: encoder, InputVideoCodec: "hevc", CRF: 0, Preset: "6", BitDepth: 10, PixelFormat: "yuv420p10le", Threads: 2, FFmpegArgs: []string{"-g", "120"}}
			shared := videoOutputArgs(plan, "")
			for _, args := range [][]string{Args(plan), SampleArgs(plan)} {
				start := slices.Index(args, "-c:v")
				if start < 0 || !slices.Equal(args[start:start+len(shared)], shared) {
					t.Fatalf("video args differ: %q", args)
				}
			}
			quality := qualityArgs(plan)
			if len(quality) == 0 || quality[len(quality)-1] != "0" {
				t.Fatalf("CRF zero lost: %q", quality)
			}
		})
	}
	if got := selectedCRF(domain.VideoProfile{CRFMin: 18}, false, &domain.SearchResult{CRF: 0}); got != 0 {
		t.Fatalf("selected CRF = %d", got)
	}
}

func TestSearchOmitsUnsafeReferenceCrop(t *testing.T) {
	if filter := ReferenceFilter(unsafeCropPlan()); filter != "" {
		t.Fatalf("unsafe reference filter = %q", filter)
	}
	args := SampleArgs(unsafeCropPlan())
	if i := slices.Index(args, "-vf"); i >= 0 && strings.Contains(args[i+1], "crop") {
		t.Fatal("sample applies unsafe crop")
	}
}

func TestOnlySamplesNormalizeTimestamps(t *testing.T) {
	plan := domain.EncodePlan{VideoCodec: "hevc_qsv", InputVideoCodec: "hevc", Accelerator: "qsv", InputWidth: 1920, InputHeight: 1080, BitDepth: 10, CropFilter: "crop=1920:1000:0:40"}
	args := SampleArgs(plan)
	i := slices.Index(args, "-vf")
	if i < 0 || !strings.HasPrefix(args[i+1], "setpts=") || !strings.Contains(args[i+1], ","+plan.CropFilter) {
		t.Fatalf("sample timestamp/crop filters = %q", args)
	}
	if !slices.Contains(args, "-xerror") || strings.Contains(strings.Join(Args(plan), " "), "setpts=") {
		t.Fatal("lost strict sample error handling or changed final encode timestamps")
	}
	// Final encodes keep the source timeline but still pass every frame to the
	// encoder, so a duplicate-timestamp or VFR source is not silently shortened.
	for _, got := range [][]string{args, Args(plan)} {
		at := slices.Index(got, "-fps_mode")
		if at < 0 || at+1 >= len(got) || got[at+1] != "passthrough" {
			t.Fatalf("fps_mode = %q", got)
		}
	}
}

func TestQSVSamplesUseSoftwareFrames(t *testing.T) {
	for _, codec := range []string{"h264", "hevc", "av1", "vp9"} {
		for _, depth := range []int{8, 10} {
			plan := domain.EncodePlan{
				InputPath: "reference.mkv", OutputPath: "candidate.mkv",
				VideoCodec: "hevc_qsv", InputVideoCodec: codec, Accelerator: "qsv",
				InputWidth: 1920, InputHeight: 1080, BitDepth: depth,
				CropFilter: "crop=1920:816:0:132", CRF: 28, Preset: "veryslow", Threads: 2,
			}
			for _, crop := range []string{plan.CropFilter, ""} {
				plan.CropFilter = crop
				args := SampleArgs(plan)
				input := slices.Index(args, "-i")
				if input < 0 || slices.Contains(args[:input], "-c:v") || slices.Contains(args, "-hwaccel") {
					t.Fatalf("sample does not use the default software decoder: %q", args)
				}
				filterAt := slices.Index(args, "-vf")
				format := "nv12"
				if depth == 10 {
					format = "p010le"
				}
				if filterAt < 0 || !strings.HasSuffix(args[filterAt+1], ",format="+format) ||
					strings.Contains(args[filterAt+1], "qsv") ||
					(crop != "" && !strings.Contains(args[filterAt+1], ","+crop+",")) {
					t.Fatalf("sample crop/format = %q", args)
				}
				encoder := slices.Index(args, "-c:v")
				if encoder < input || args[encoder+1] != plan.VideoCodec {
					t.Fatalf("sample lost QSV encoding: %q", args)
				}
				final := Args(plan)
				decoder := slices.Index(final, "-c:v")
				if decoder < 0 || final[decoder+1] != codec+"_qsv" || !slices.Contains(final, "-hwaccel") {
					t.Fatalf("final encode lost QSV decoding: %q", final)
				}
			}
		}
	}
}

func TestEncoderArgumentsCannotOverrideSearch(t *testing.T) {
	for _, args := range [][]string{
		{"-crf", "20"}, {"-vstats_file", "/tmp/x"}, {"-attach", "cover.jpg"}, {"-dump_attachment", "x"},
		{"-i", "other.mkv"}, {"-f", "matroska"}, {"-map", "0:v"}, {"-filter_complex", "null"},
		{"-vf", "scale=128:128"}, {"-passlogfile", "/tmp/pass"}, {"-progress", "pipe:1"}, {"-y", "output.mkv"},
		{"output.mkv"}, {"-tune"}, {"-c:v", "copy"}, {"-r", "25"}, {"-tune", "-film"},
	} {
		if err := ValidateEncoderArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	for _, args := range [][]string{
		{"-svtav1-params", "tune=0:film-grain=8", "-g", "120"},
		{"-svtav1-params", "lp=2:keyint=120"},
		{"-x265-params", "aq-mode=3:info=0"},
		{"-x264-params", "ref=4:me=umh"},
	} {
		if err := ValidateEncoderArgs(args); err != nil {
			t.Fatalf("rejected %q: %v", args, err)
		}
	}
	// The codec params string is applied after the FFmpeg-level options, so a
	// managed key there would silently override the searched CRF or preset.
	for _, args := range [][]string{
		{"-svtav1-params", "crf=60"},
		{"-svtav1-params", "preset=4:film-grain=8"},
		{"-svtav1-params", "rc=1:tbr=8000"},
		{"-x265-params", "crf=20:info=0"},
		{"-x265-params", "qp=18"},
		{"-x264-params", "qpmin=10:ref=4"},
		{"-x264-params", "bitrate=5000"},
		{"-svtav1-params", "CRF=60"},
	} {
		if err := ValidateEncoderArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestEncoderParamBundlesAreAnAllowList(t *testing.T) {
	accepted := [][]string{
		{"-svtav1-params", "tune=0:film-grain=8:film-grain-denoise=0:enable-qm=1:qm-min=0:qm-max=8"},
		{"-svtav1-params", "enable-variance-boost=1:variance-boost-strength=2:variance-octile=6:sharpness=-2:scd=1:lookahead=-1"},
		{"-svtav1-params", "color-primaries=9:transfer-characteristics=16:matrix-coefficients=9:color-range=0:chroma-sample-position=topleft"},
		{"-svtav1-params", "mastering-display=G(0.2650,0.6900)B(0.1500,0.0600)R(0.6800,0.3200)WP(0.3127,0.3290)L(1000.0,0.0001):content-light=1000,400"},
		{"-x265-params", "aq-mode=3:psy-rd=2.0:psy-rdoq=1.0:deblock=-1,-1:no-sao=1:no-strong-intra-smoothing=1:log-level=error"},
		{"-x265-params", "keyint=24:min-keyint=24:open-gop=0:no-cutree=1:ctu=32:hdr10=1:hdr10-opt=1:repeat-headers=1"},
		{"-x265-params", "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:range=limited:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400"},
		{"-x264-params", "open-gop=1:keyint=48:min-keyint=48:scenecut=0:bframes=3:no-mbtree=1:psy-rd=1.0,0.15:partitions=p8x8,b8x8"},
		{"-x265-params:v", "info=0"},
	}
	for _, args := range accepted {
		if err := ValidateEncoderArgs(args); err != nil {
			t.Errorf("rejected %q: %v", args, err)
		}
	}
	refused := []struct {
		args []string
		want string
	}{
		// Keys that read or write a file.
		{[]string{"-x264-params", "dump-yuv=/tmp/out.yuv"}, `"dump-yuv=/tmp/out.yuv" may only contain`},
		{[]string{"-x264-params", "dump-yuv=out.yuv"}, `"dump-yuv" is not accepted in -x264-params`},
		{[]string{"-x264-params", "stats=x264.log"}, `"stats" is not accepted`},
		{[]string{"-x264-params", "cqmfile=matrix.cfg"}, `"cqmfile" is not accepted`},
		{[]string{"-x265-params", "csv=log.csv"}, `"csv" is not accepted`},
		{[]string{"-x265-params", "analysis-save=analysis.dat"}, `"analysis-save" is not accepted`},
		{[]string{"-x265-params", "film-grain=model.bin"}, `"film-grain" is not accepted in -x265-params`},
		{[]string{"-svtav1-params", "fgs-table=table.tbl"}, `"fgs-table" is not accepted`},
		{[]string{"-svtav1-params", "stats=svt.log"}, `"stats" is not accepted`},
		// A key of another encoder.
		{[]string{"-svtav1-params", "aq-strength=1.0"}, `"aq-strength" is not accepted in -svtav1-params`},
		{[]string{"-svtav1-params", "no-scd=1"}, `"no-scd" is not accepted`},
		// Quality, rate control, and speed stay with anvil.
		{[]string{"-x265-params", "pass=1"}, `"pass" overrides a setting managed by Anvil`},
		{[]string{"-x264-params", "vbv-maxrate=5000"}, `"vbv-maxrate" overrides`},
		{[]string{"-x265-params", "crf_max=30"}, `"crf_max" overrides`},
		{[]string{"-svtav1-params", "Preset=4"}, `"Preset" overrides`},
		// av_dict_parse_string unescapes, unquotes, and trims, so each of
		// these reaches the encoder as crf.
		{[]string{"-svtav1-params", `c\rf=51`}, `may only contain`},
		{[]string{"-svtav1-params", "'crf'=60"}, `may only contain`},
		{[]string{"-svtav1-params", `"crf"=60`}, `may only contain`},
		{[]string{"-svtav1-params", "tune=0: crf=60"}, `may only contain`},
		{[]string{"-svtav1-params", "tune=0:crf =60"}, `may only contain`},
		{[]string{"-svtav1-params", "tune=0:\tcrf=60"}, `may only contain`},
		{[]string{"-svtav1-params", "tune=0\ncrf=60"}, `may only contain`},
		{[]string{"-svtav1-params", `tune=0\:crf=60`}, `may only contain`},
		{[]string{"-x265-params", "colorprim='bt709'"}, `may only contain`},
		// Shapes ffmpeg would refuse or read differently.
		{[]string{"-x265-params", "sao"}, `"sao" needs a value`},
		{[]string{"-x265-params", "sao="}, `"sao" needs a value`},
		{[]string{"-x265-params", "sao=1::info=0"}, `empty entry`},
		{[]string{"-x265-params", "sao=1:"}, `empty entry`},
		{[]string{"-x265-params", "deblock=1=2"}, `may only contain`},
	}
	for _, test := range refused {
		err := ValidateEncoderArgs(test.args)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("ValidateEncoderArgs(%q) = %v, want an error containing %s", test.args, err, test.want)
		}
	}
}

func TestEncoderArgumentValuesMayBeNegativeNumbers(t *testing.T) {
	for _, args := range [][]string{{"-g", "-1"}, {"-bf", "-1"}, {"-sc_threshold", "-1"}, {"-aq-strength", "-0.5"}} {
		if err := ValidateEncoderArgs(args); err != nil {
			t.Errorf("rejected %q: %v", args, err)
		}
	}
	for _, args := range [][]string{{"-g", "-i"}, {"-g", "-1x"}, {"-g", "-"}, {"-g", "--1"}, {"-g", "-1.5.2"}, {"-tune", "-inf"}} {
		if err := ValidateEncoderArgs(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}
