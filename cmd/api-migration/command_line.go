package main

import "strings"

// commandLine renders arguments for a POSIX shell without expanding their content.
func commandLine(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg != "" && strings.IndexFunc(arg, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') &&
				!strings.ContainsRune("_@%+=:,./-", r)
		}) == -1 {
			quoted[i] = arg
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// stateCommand uses the executable actually invoked by the administrator. The
// in-process test harness uses the conventional name when no path was supplied.
func (p *Procedure) stateCommand(command string) string {
	program := p.program
	if program == "" {
		program = "api-migration"
	}
	stateDir := "<state>"
	if p.state != nil {
		stateDir = p.state.dir
	}
	return commandLine(program, command, "--state", stateDir)
}
