package main

// This file uses only the standard library. The shell runner copies it into
// each isolated upstream parser module so dependency versions cannot leak from
// the operator module into the parser under test.
import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type preparedCase struct {
	File          string            `json:"file"`
	WantFailure   bool              `json:"want_failure"`
	ErrorContains []string          `json:"error_contains,omitempty"`
	Assertions    map[string]string `json:"assertions,omitempty"`
	Problems      []string          `json:"problems,omitempty"`
}

type preparedMatrix struct {
	Version string         `json:"version"`
	Commit  string         `json:"commit"`
	Role    string         `json:"role"`
	Cases   []preparedCase `json:"cases"`
}

func checkResult(test preparedCase, parsed any, diagnostics []string) error {
	problems := append(slices.Clone(test.Problems), diagnostics...)
	if !test.WantFailure && parsed != nil {
		for _, path := range sortedAssertionPaths(test.Assertions) {
			got, err := parsedValue(parsed, path)
			if err != nil {
				problems = append(problems, fmt.Sprintf("assert %s: %v", path, err))
			} else if !sameAssertionValue(got, test.Assertions[path]) {
				problems = append(problems, fmt.Sprintf("assert %s: got %q, want %q", path, got, test.Assertions[path]))
			}
		}
	}
	if !test.WantFailure {
		if len(problems) > 0 {
			return fmt.Errorf("unexpected validation failure: %s", strings.Join(problems, "; "))
		}
		return nil
	}
	if len(problems) == 0 {
		return fmt.Errorf("negative case passed without detecting %v", test.ErrorContains)
	}
	for _, problem := range problems {
		matched := false
		for _, expected := range test.ErrorContains {
			if strings.Contains(problem, expected) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("unrelated failure in negative case: %s", problem)
		}
	}
	return nil
}

// Some upstream options contain serialized JSON. Compare their decoded values
// because the parser does not preserve object key order.
func sameAssertionValue(got, want string) bool {
	if got == want {
		return true
	}
	if !strings.HasPrefix(want, "{") && !strings.HasPrefix(want, "[") {
		return false
	}
	var gotJSON, wantJSON any
	if json.Unmarshal([]byte(got), &gotJSON) != nil || json.Unmarshal([]byte(want), &wantJSON) != nil {
		return false
	}
	return reflect.DeepEqual(gotJSON, wantJSON)
}

func sortedAssertionPaths(assertions map[string]string) []string {
	paths := make([]string, 0, len(assertions))
	for path := range assertions {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func parsedValue(parsed any, path string) (string, error) {
	value := reflect.ValueOf(parsed)
	parts := strings.Split(path, ".")
	for position, part := range parts {
		for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
			value = value.Elem()
		}
		if !value.IsValid() {
			return "", fmt.Errorf("nil value at %s", part)
		}
		if part == "#" {
			if position != len(parts)-1 {
				return "", fmt.Errorf("collection length must end the assertion path")
			}
			switch value.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map:
				return strconv.Itoa(value.Len()), nil
			default:
				return "", fmt.Errorf("%s is not a collection", path)
			}
		}
		switch value.Kind() {
		case reflect.Struct:
			value = exportedField(value, part)
		case reflect.Map:
			if value.Type().Key().Kind() != reflect.String {
				return "", fmt.Errorf("%s has non-string keys", path)
			}
			value = value.MapIndex(reflect.ValueOf(part))
		case reflect.Slice, reflect.Array:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= value.Len() {
				return "", fmt.Errorf("invalid index %q", part)
			}
			value = value.Index(index)
		default:
			return "", fmt.Errorf("cannot select %s", part)
		}
	}
	if !value.IsValid() || !value.CanInterface() {
		return "", fmt.Errorf("parsed field is missing or inaccessible")
	}
	return fmt.Sprint(value.Interface()), nil
}

// exportedField reads exported data fields, including promoted fields. It does
// not resolve or invoke methods from an assertion path.
func exportedField(value reflect.Value, name string) reflect.Value {
	// A direct field takes precedence over promoted fields.
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if field.IsExported() && field.Name == name {
			return value.Field(i)
		}
	}
	var result reflect.Value
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		child := value.Field(i)
		if !field.Anonymous {
			continue
		}
		if child.Kind() == reflect.Pointer {
			child = child.Elem()
		}
		if child.IsValid() && child.Kind() == reflect.Struct {
			if found := exportedField(child, name); found.IsValid() {
				if result.IsValid() {
					return reflect.Value{}
				}
				result = found
			}
		}
	}
	return result
}
