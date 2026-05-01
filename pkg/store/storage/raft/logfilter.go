package raftbackend

// raft-boltdb's transaction-management code paths log a benign
// "Rollback failed: tx closed" line via the standard library
// logger (defer-rollback racing the commit on already-closed txs).
// It's harmless but it pollutes operator logs. We replace stdlog's
// default writer with a filter that drops only that exact pattern
// and forwards everything else to stderr.
//
// This same filter previously lived in pkg/store/raft (the
// pre-Plan-2 backend) and was lost when the production path moved
// to pkg/store/storage/raft. Set HULA_LOG_VERBOSE_NOISE=1 to keep
// the lines flowing for debugging.

import (
	stdlog "log"
	"os"
	"strings"
)

var noiseVerbose = os.Getenv("HULA_LOG_VERBOSE_NOISE") == "1"

type filteredStdlogWriter struct{}

func (filteredStdlogWriter) Write(p []byte) (int, error) {
	if !noiseVerbose && strings.Contains(string(p), "Rollback failed") {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func init() {
	stdlog.SetOutput(filteredStdlogWriter{})
}
