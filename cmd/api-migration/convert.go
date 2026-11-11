package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// convert rewrites the OpenBao resources in manifest files from openbao.org to
// kubebao.org, for the Git repository a GitOps controller syncs. It changes
// only the top-level apiVersion line of a YAML document whose top-level kind
// is an OpenBao resource, so comments, formatting, and template syntax stay.

var (
	documentSeparator = regexp.MustCompile(`^---\s*(#.*)?$`)
	sourceAPIVersion  = regexp.MustCompile(`^apiVersion:\s*(["']?)openbao\.org/v1alpha1(["']?)(\s*(#.*)?)$`)
	openBaoKind       = regexp.MustCompile(`^kind:\s*["']?(OpenBaoCluster|OpenBaoTenant|OpenBaoRestore)["']?\s*(#.*)?$`)
)

// convertManifest returns the converted content and the objects it changed.
func convertManifest(content string) (string, int) {
	lines := strings.SplitAfter(content, "\n")
	changed := 0
	start := 0
	flush := func(end int) {
		kind, version := false, -1
		for i := start; i < end; i++ {
			line := strings.TrimRight(lines[i], "\r\n")
			if openBaoKind.MatchString(line) {
				kind = true
			}
			if sourceAPIVersion.MatchString(line) {
				version = i
			}
		}
		if kind && version >= 0 {
			line := lines[version]
			newline := line[len(strings.TrimRight(line, "\r\n")):]
			lines[version] = sourceAPIVersion.ReplaceAllString(strings.TrimRight(line, "\r\n"),
				"apiVersion: ${1}kubebao.org/v1alpha1${2}${3}") + newline
			changed++
		}
	}
	for i, line := range lines {
		if documentSeparator.MatchString(strings.TrimRight(line, "\r\n")) {
			flush(i)
			start = i + 1
		}
	}
	flush(len(lines))
	return strings.Join(lines, ""), changed
}

// runConvert implements `api-migration convert [--write] FILE...`.
func runConvert(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("convert", flag.ContinueOnError)
	flags.SetOutput(stderr)
	write := flags.Bool("write", false, "rewrite the files in place instead of listing the changes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("convert needs at least one manifest file")
	}
	total := 0
	for _, path := range flags.Args() {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		converted, changed := convertManifest(string(data))
		if changed == 0 {
			continue
		}
		total += changed
		if *write {
			if err := os.WriteFile(path, []byte(converted), info.Mode().Perm()); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(stdout, "%s: %d object(s)\n", path, changed)
	}
	if !*write && total > 0 {
		_, _ = fmt.Fprintln(stdout, "run again with --write to rewrite these files")
	}
	return nil
}
