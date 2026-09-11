package manager

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDownloadProgressRender(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "plain"
		columns := 0
		if terminal {
			name, columns = "terminal", 80
		}
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			started := time.Unix(0, 0)
			p := &downloadProgress{out: &out, total: 2048, started: started, columns: columns}
			p.Write(make([]byte, 1024))
			p.render(started.Add(2*time.Second), "")
			first := out.String()
			for _, want := range []string{"50%", "1.0 KiB/2.0 KiB", "512 B/s"} {
				if !strings.Contains(first, want) {
					t.Errorf("progress %q does not contain %q", first, want)
				}
			}
			if terminal && (!strings.HasPrefix(first, "\r") || strings.Contains(first, "\n") || !strings.Contains(first, "[==========----------]")) {
				t.Errorf("terminal progress should redraw one unfinished line: %q", first)
			}
			p.Write(make([]byte, 1024))
			p.render(started.Add(4*time.Second), "done")
			got := out.String()
			if !strings.Contains(got, "100%") || !strings.HasSuffix(got, "done\n") {
				t.Errorf("missing completed progress: %q", got)
			}
			if terminal {
				if strings.Count(got, "\r") != 2 || strings.Count(got, "\n") != 1 {
					t.Errorf("terminal output should end its progress line only once: %q", got)
				}
			} else if strings.ContainsAny(got, "\r\x1b") || strings.Count(got, "\n") != 2 {
				t.Errorf("plain output should contain ordinary lines: %q", got)
			}
		})
	}
}

func TestDownloadProgressDoesNotRoundUpToComplete(t *testing.T) {
	var out bytes.Buffer
	p := &downloadProgress{out: &out, total: 1000, started: time.Unix(0, 0)}
	p.Write(make([]byte, 999))
	p.render(p.started.Add(time.Second), "")
	if got := out.String(); !strings.Contains(got, "99%") || strings.Contains(got, "100%") {
		t.Fatalf("partial transfer should remain below 100%%: %q", got)
	}
}
