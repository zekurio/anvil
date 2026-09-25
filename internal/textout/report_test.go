package textout

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReportRedirectAndWriteFailure(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	var out bytes.Buffer
	if err := WriteReport(&out, func(w *Writer) {
		w.Heading("Jobs")
		w.Table([]string{"State", "File"}, [][]string{{w.State("failed"), "episode.mkv"}})
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("forced color leaked into redirected output")
	}
	broken := errors.New("broken pipe")
	if err := WriteReport(failedWriter{broken}, func(w *Writer) { w.Heading("Jobs"); w.Field("Count", 2) }); !errors.Is(err, broken) {
		t.Fatalf("lost write failure: %v", err)
	}
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }
