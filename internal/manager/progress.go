package manager

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Only the reporter goroutine writes while a download is running. finish waits
// for it before printing the final line, so subsequent messages cannot overlap.
type downloadProgress struct {
	out     io.Writer
	total   int64
	current atomic.Int64
	started time.Time
	columns int
	width   int
	stop    chan struct{}
	done    chan struct{}
}

func startDownloadProgress(out io.Writer, total int64) *downloadProgress {
	p := &downloadProgress{
		out: out, total: total, started: time.Now(), columns: terminalColumns(out),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	p.render(p.started, "")
	interval := 5 * time.Second
	if p.columns > 0 {
		interval = 200 * time.Millisecond
	}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				p.render(now, "")
			case <-p.stop:
				return
			}
		}
	}()
	return p
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	p.current.Add(int64(len(data)))
	return len(data), nil
}

func (p *downloadProgress) finish(success bool) {
	close(p.stop)
	<-p.done
	status := "failed"
	if success {
		status = "done"
	}
	p.render(time.Now(), status)
}

func (p *downloadProgress) render(now time.Time, status string) {
	n := p.current.Load()
	fraction := 0.0
	if p.total > 0 {
		fraction = min(1, float64(n)/float64(p.total))
	}
	rate := 0.0
	if elapsed := now.Sub(p.started).Seconds(); elapsed > 0 {
		rate = float64(n) / elapsed
	}
	// Do not round a partial transfer up to 100%.
	detail := fmt.Sprintf("%3d%% %s/%s  %s/s", int(fraction*100), byteSize(float64(n)), byteSize(float64(p.total)), byteSize(rate))
	if status != "" {
		detail += " " + status
	}
	if p.columns == 0 {
		fmt.Fprintln(p.out, "  "+detail)
		return
	}
	barWidth := min(20, p.columns-len(detail)-6)
	line := "  " + detail
	if barWidth > 0 {
		filled := int(fraction * float64(barWidth))
		line = "  [" + strings.Repeat("=", filled) + strings.Repeat("-", barWidth-filled) + "] " + detail
	}
	// Avoid wrapping on narrow terminals, which would defeat carriage returns.
	if len(line) >= p.columns {
		line = line[:max(0, p.columns-1)]
	}
	fmt.Fprint(p.out, "\r"+line+strings.Repeat(" ", max(0, p.width-len(line))))
	p.width = len(line)
	if status != "" {
		fmt.Fprintln(p.out)
	}
}

func byteSize(n float64) string {
	units := [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

func terminalColumns(out io.Writer) int {
	f, ok := out.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" {
		return 0
	}
	var size struct{ rows, columns, xpixel, ypixel uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	runtime.KeepAlive(f)
	if errno != 0 {
		return 0
	}
	if size.columns == 0 {
		return 80
	}
	return int(size.columns)
}
