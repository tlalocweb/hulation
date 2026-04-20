package sitedeploy

import (
	"fmt"
	"strings"
)

// Protocol message prefixes for communication between hula and hulabuild.
// hulabuild runs inside the builder container. Communication is via
// the container's stdin/stdout streams.

// Messages sent from hulabuild -> hula (on stdout)
const (
	// MsgSendTarballTo requests hula to docker-cp the site source tarball
	// into the container at the specified directory.
	// Format: SEND_TARBALL_TO <dir>
	MsgSendTarballTo = "SEND_TARBALL_TO"

	// MsgOutboundTarballReady tells hula that the built site tarball is ready
	// at the specified path inside the container.
	// Format: OUTBOUND_TARBALL_READY <path>
	MsgOutboundTarballReady = "OUTBOUND_TARBALL_READY"

	// MsgBuildLog is a log line from the build process.
	// Format: BUILD_LOG <message>
	MsgBuildLog = "BUILD_LOG"

	// MsgBuildError indicates a fatal build error.
	// Format: BUILD_ERROR <message>
	MsgBuildError = "BUILD_ERROR"

	// MsgReady indicates hulabuild has entered the staging loop and is
	// ready to receive EXEC_BUILD commands.
	MsgReady = "READY"

	// MsgBuildDone indicates a staging rebuild completed successfully.
	MsgBuildDone = "BUILD_DONE"
)

// Messages sent from hula -> hulabuild (on stdin)
const (
	// MsgInboundTarballReady tells hulabuild that the source tarball has been
	// copied into the container and is ready to be extracted.
	MsgInboundTarballReady = "INBOUND_TARBALL_READY"

	// MsgExecBuild tells hulabuild to run a build command in the site directory.
	// Used in staging mode for rebuilds.
	// Format: EXEC_BUILD <command>
	MsgExecBuild = "EXEC_BUILD"

	// MsgShutdown tells hulabuild to exit gracefully.
	MsgShutdown = "SHUTDOWN"
)

// ParseProtocolMessage parses a protocol message line into its command and argument.
// Returns the command prefix and the remaining argument string.
func ParseProtocolMessage(line string) (cmd string, arg string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("empty protocol message")
	}
	parts := strings.SplitN(line, " ", 2)
	cmd = parts[0]
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg, nil
}

// FormatProtocolMessage formats a protocol message with command and argument.
func FormatProtocolMessage(cmd string, arg string) string {
	if arg == "" {
		return cmd + "\n"
	}
	return cmd + " " + arg + "\n"
}
