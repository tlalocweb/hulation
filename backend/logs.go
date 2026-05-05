package backend

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/tlalocweb/hulation/log"
)

// LogConfig controls passthrough of backend container stdout/stderr
// into hula's own log stream. Defaults: passthrough on, colorized.
type LogConfig struct {
	// Set true to disable passthrough entirely. Default: passthrough on.
	Disabled bool `yaml:"disabled,omitempty"`
	// Set true to disable ANSI colorization of the per-container
	// prefix. Default: colorized (matches docker compose v2 output).
	NoColor bool `yaml:"no_color,omitempty"`
}

// Effective returns enabled+colored after applying defaults.
// nil receiver → both true (passthrough on, colorized).
func (l *LogConfig) Effective() (enabled, colored bool) {
	if l == nil {
		return true, true
	}
	return !l.Disabled, !l.NoColor
}

// streamContainerLogs follows the container's stdout/stderr and
// forwards each line into hula's log stream prefixed with the
// container name. Returns immediately; the goroutine exits when the
// log stream EOFs (container removed/stopped) or ctx is cancelled.
//
// containers are created without a TTY (see types.go ToContainerConfig),
// so the log stream is always the multiplexed format that stdcopy
// understands.
func streamContainerLogs(ctx context.Context, cli *client.Client, name string, colored bool) {
	go func() {
		opts := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Tail:       "0",
		}
		rc, err := cli.ContainerLogs(ctx, name, opts)
		if err != nil {
			log.Warnf("backend: log passthrough for %s could not start: %s", name, err)
			return
		}
		defer rc.Close()

		prefix := buildPrefix(name, colored)
		stdoutW := newPrefixingWriter(prefix, false)
		stderrW := newPrefixingWriter(prefix, true)
		// stdcopy demuxes the docker frame format into stdout/stderr.
		// Errors here are usually "context cancelled" or "container
		// gone" on shutdown — log at debug level only.
		if _, err := stdcopy.StdCopy(stdoutW, stderrW, rc); err != nil && ctx.Err() == nil {
			log.Debugf("backend: log passthrough for %s ended: %s", name, err)
		}
		stdoutW.Flush()
		stderrW.Flush()
	}()
}

// docker compose v2's per-service color palette: bright foreground
// ANSI 8-color set, skipping black/white/grey (low contrast on most
// terminals). Order matches compose's `formatter.Palette()`.
var colorPalette = []string{
	"\x1b[36m", // cyan
	"\x1b[33m", // yellow
	"\x1b[32m", // green
	"\x1b[35m", // magenta
	"\x1b[34m", // blue
	"\x1b[31m", // red
	"\x1b[96m", // bright cyan
	"\x1b[93m", // bright yellow
	"\x1b[92m", // bright green
	"\x1b[95m", // bright magenta
	"\x1b[94m", // bright blue
	"\x1b[91m", // bright red
}

const ansiReset = "\x1b[0m"

// buildPrefix returns the per-container log-line prefix. When
// colored, picks a stable palette entry from the container name so
// the same container is always the same color across restarts.
func buildPrefix(name string, colored bool) string {
	if !colored {
		return "[" + name + "] "
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	c := colorPalette[h.Sum32()%uint32(len(colorPalette))]
	return c + "[" + name + "]" + ansiReset + " "
}

// prefixingWriter wraps lines from the container's log stream and
// forwards them — one log call per line — into hula's logger. stderr
// frames go through log.Warnf so they stand out; stdout uses Infof.
//
// The writer buffers a partial line until a newline arrives so that
// half-lines from short writes don't fragment in the output.
type prefixingWriter struct {
	mu     sync.Mutex
	buf    []byte
	prefix string
	stderr bool
}

func newPrefixingWriter(prefix string, stderr bool) *prefixingWriter {
	return &prefixingWriter{prefix: prefix, stderr: stderr}
}

func (w *prefixingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := indexNewline(w.buf)
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		w.emit(line)
	}
	return len(p), nil
}

// Flush emits any partial line still buffered (e.g. on EOF).
func (w *prefixingWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 {
		return
	}
	w.emit(strings.TrimRight(string(w.buf), "\r"))
	w.buf = w.buf[:0]
}

func (w *prefixingWriter) emit(line string) {
	if line == "" {
		return
	}
	if w.stderr {
		log.Warnf("%s%s", w.prefix, line)
	} else {
		log.Infof("%s%s", w.prefix, line)
	}
}

func indexNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}
