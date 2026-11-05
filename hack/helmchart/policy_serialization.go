package main

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// literalPolicyExpressions preserves parsed CEL values while replacing folded
// scalars, which YAML post-renderers can re-emit with additional newlines.
func literalPolicyExpressions(content string) (string, error) {
	lines := strings.Split(content, "\n")
	var result []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimPrefix(strings.TrimSpace(line), "- ") != "expression: >-" {
			result = append(result, line)
			continue
		}

		column := strings.Index(line, "expression:")
		end := i + 1
		for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || len(leadingWhitespace(lines[end])) > column) {
			end++
		}
		bodyEnd := end
		for bodyEnd > i+1 && strings.TrimSpace(lines[bodyEnd-1]) == "" {
			bodyEnd--
		}
		var expression string
		if err := yaml.Unmarshal([]byte(">-\n"+strings.Join(lines[i+1:bodyEnd], "\n")+"\n"), &expression); err != nil {
			return "", fmt.Errorf("decode expression at line %d: %w", i+1, err)
		}
		result = append(result, line[:column]+"expression: |-")
		for _, part := range strings.Split(expression, "\n") {
			if part != "" {
				part = strings.Repeat(" ", column+2) + part
			}
			result = append(result, part)
		}
		result = append(result, lines[bodyEnd:end]...)
		i = end - 1
	}
	return strings.Join(result, "\n"), nil
}
