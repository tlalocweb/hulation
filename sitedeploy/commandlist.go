package sitedeploy

import (
	"fmt"
	"strings"
)

// Valid command keywords in a COMMANDLIST.
var validCommands = map[string]bool{
	"WORKDIR":  true,
	"HUGO":     true,
	"ASTRO":    true,
	"GATSBY":   true,
	"MKDOCS":   true,
	"CP":       true,
	"RM":       true,
	"RUN":      true,
	"FINALIZE": true,
}

// Command represents a single parsed command from a COMMANDLIST.
type Command struct {
	Name string
	Args string
	Line int // 1-based line number in the command list
}

// ParseCommandList parses a command list string into a slice of Commands.
// Each line is a command. Empty lines and lines starting with # are skipped.
func ParseCommandList(text string) ([]Command, error) {
	var commands []Command
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		name := strings.ToUpper(parts[0])
		if !validCommands[name] {
			return nil, fmt.Errorf("line %d: unknown command %q", i+1, parts[0])
		}
		var args string
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}
		commands = append(commands, Command{
			Name: name,
			Args: args,
			Line: i + 1,
		})
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("empty command list")
	}
	return commands, nil
}

// ValidateCommandList checks structural rules for a parsed command list.
func ValidateCommandList(commands []Command) error {
	if len(commands) == 0 {
		return fmt.Errorf("empty command list")
	}

	hasWorkdir := false
	hasFinalize := false
	for i, cmd := range commands {
		switch cmd.Name {
		case "WORKDIR":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: WORKDIR requires a directory argument", cmd.Line)
			}
			hasWorkdir = true
		case "FINALIZE":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: FINALIZE requires a directory argument", cmd.Line)
			}
			if i != len(commands)-1 {
				return fmt.Errorf("line %d: FINALIZE must be the last command", cmd.Line)
			}
			hasFinalize = true
		case "CP":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: CP requires arguments", cmd.Line)
			}
		case "RM":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: RM requires arguments", cmd.Line)
			}
		case "RUN":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: RUN requires a command", cmd.Line)
			}
		}
	}

	if !hasWorkdir {
		return fmt.Errorf("command list must include a WORKDIR command")
	}
	if !hasFinalize {
		return fmt.Errorf("command list must end with a FINALIZE command")
	}
	return nil
}

// ValidateCommandListForStaging checks structural rules for a staging command list.
// Staging profiles require WORKDIR but do NOT require FINALIZE.
func ValidateCommandListForStaging(commands []Command) error {
	if len(commands) == 0 {
		return fmt.Errorf("empty command list")
	}

	hasWorkdir := false
	for _, cmd := range commands {
		switch cmd.Name {
		case "WORKDIR":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: WORKDIR requires a directory argument", cmd.Line)
			}
			hasWorkdir = true
		case "FINALIZE":
			return fmt.Errorf("line %d: FINALIZE is not allowed in staging profiles", cmd.Line)
		case "CP":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: CP requires arguments", cmd.Line)
			}
		case "RM":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: RM requires arguments", cmd.Line)
			}
		case "RUN":
			if cmd.Args == "" {
				return fmt.Errorf("line %d: RUN requires a command", cmd.Line)
			}
		}
	}

	if !hasWorkdir {
		return fmt.Errorf("command list must include a WORKDIR command")
	}
	return nil
}
