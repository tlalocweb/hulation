package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	configFile := flag.String("c", "", "path to hulabuild config file (optional)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: hulabuild [-c CONFIG] [COMMANDLIST_FILE]\n\n")
		fmt.Fprintf(os.Stderr, "Executes a build command list inside a hula builder container.\n")
		fmt.Fprintf(os.Stderr, "Commands are read from COMMANDLIST_FILE, or from stdin if no file is given.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nSupported commands:\n")
		fmt.Fprintf(os.Stderr, "  WORKDIR <dir>      Set working directory, trigger site source transfer\n")
		fmt.Fprintf(os.Stderr, "  HUGO [flags]       Run Hugo static site generator\n")
		fmt.Fprintf(os.Stderr, "  ASTRO [flags]      Run Astro static site generator\n")
		fmt.Fprintf(os.Stderr, "  GATSBY [flags]     Run Gatsby static site generator\n")
		fmt.Fprintf(os.Stderr, "  MKDOCS [flags]     Run MkDocs static site generator\n")
		fmt.Fprintf(os.Stderr, "  CP <args>          Copy files (sandboxed to WORKDIR)\n")
		fmt.Fprintf(os.Stderr, "  RM <args>          Remove files (sandboxed to WORKDIR)\n")
		fmt.Fprintf(os.Stderr, "  RUN <command>      Run arbitrary shell command\n")
		fmt.Fprintf(os.Stderr, "  FINALIZE <dir>     Tarball directory and signal completion\n")
	}
	flag.Parse()

	_ = configFile // reserved for future use

	// Read command list
	var commandText string
	if flag.NArg() > 0 {
		data, err := os.ReadFile(flag.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading command file: %s\n", err)
			os.Exit(1)
		}
		commandText = string(data)
	} else {
		// Read from stdin until we see a blank line or EOF
		// The protocol: first the command list is sent, terminated by a blank line,
		// then the rest of stdin is used for protocol messages.
		data, err := readCommandsFromStdin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading commands from stdin: %s\n", err)
			os.Exit(1)
		}
		commandText = data
	}

	// Parse commands
	commands, err := parseCommands(commandText)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing commands: %s\n", err)
		os.Exit(1)
	}

	// Set up protocol I/O
	proto := newProtocolIO(os.Stdin, os.Stdout)
	exec := newExecutor(proto)

	// Execute commands in order
	for _, cmd := range commands {
		proto.sendLog(">>> %s %s", cmd.name, cmd.args)
		if err := exec.run(cmd.name, cmd.args); err != nil {
			proto.sendError("command %s failed: %s", cmd.name, err)
			os.Exit(1)
		}
	}
}

type parsedCommand struct {
	name string
	args string
}

func parseCommands(text string) ([]parsedCommand, error) {
	var commands []parsedCommand
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		name := strings.ToUpper(parts[0])
		var args string
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}
		commands = append(commands, parsedCommand{name: name, args: args})
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("no commands found")
	}
	return commands, nil
}

// readCommandsFromStdin reads the command list from stdin.
// When hulabuild is used as a container entrypoint with stdin attached,
// the command list is sent first, terminated by a line containing only "---".
// After the separator, the stdin stream is used for protocol messages.
func readCommandsFromStdin() (string, error) {
	var lines []string
	buf := make([]byte, 1)
	var line strings.Builder

	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				l := line.String()
				if l == "---" {
					// End of command list
					break
				}
				lines = append(lines, l)
				line.Reset()
			} else {
				line.WriteByte(buf[0])
			}
		}
		if err != nil {
			if err == io.EOF {
				// If we hit EOF without seeing ---, use what we have
				if line.Len() > 0 {
					lines = append(lines, line.String())
				}
				break
			}
			return "", err
		}
	}

	return strings.Join(lines, "\n"), nil
}
