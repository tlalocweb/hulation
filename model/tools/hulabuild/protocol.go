package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Protocol message constants - must match sitedeploy/protocol.go
const (
	// Outbound (hulabuild -> hula)
	msgSendTarballTo        = "SEND_TARBALL_TO"
	msgOutboundTarballReady = "OUTBOUND_TARBALL_READY"
	msgBuildLog             = "BUILD_LOG"
	msgBuildError           = "BUILD_ERROR"
	msgReady                = "READY"
	msgBuildDone            = "BUILD_DONE"

	// Inbound (hula -> hulabuild)
	msgInboundTarballReady = "INBOUND_TARBALL_READY"
	msgExecBuild           = "EXEC_BUILD"
	msgShutdown            = "SHUTDOWN"
)

type protocolIO struct {
	writer io.Writer
	reader *bufio.Scanner
}

func newProtocolIO(r io.Reader, w io.Writer) *protocolIO {
	return &protocolIO{
		writer: w,
		reader: bufio.NewScanner(r),
	}
}

// send writes a protocol message to hula via stdout.
func (p *protocolIO) send(cmd, arg string) error {
	var msg string
	if arg == "" {
		msg = cmd + "\n"
	} else {
		msg = cmd + " " + arg + "\n"
	}
	_, err := fmt.Fprint(p.writer, msg)
	return err
}

// sendLog sends a BUILD_LOG message.
func (p *protocolIO) sendLog(format string, args ...interface{}) error {
	msg := fmt.Sprintf(format, args...)
	return p.send(msgBuildLog, msg)
}

// sendError sends a BUILD_ERROR message.
func (p *protocolIO) sendError(format string, args ...interface{}) error {
	msg := fmt.Sprintf(format, args...)
	return p.send(msgBuildError, msg)
}

// waitFor reads lines from stdin until it sees a message with the expected command prefix.
// Returns the argument portion of the matching message.
func (p *protocolIO) waitFor(expectedCmd string) (string, error) {
	for p.reader.Scan() {
		line := strings.TrimSpace(p.reader.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if parts[0] == expectedCmd {
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1]), nil
			}
			return "", nil
		}
	}
	if err := p.reader.Err(); err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return "", fmt.Errorf("stdin closed while waiting for %s", expectedCmd)
}
