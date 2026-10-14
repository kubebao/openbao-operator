// Package configschema inventories the configuration paths used by the operator
// and the corresponding upstream parser and Raft backend sources.
package configschema

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Keys is a set of qualified configuration paths.
type Keys map[string]bool

// Sorted returns the paths in lexical order.
func (k Keys) Sorted() []string {
	paths := make([]string, 0, len(k))
	for path := range k {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

type source struct {
	structs   map[string]*ast.StructType
	constants map[string]string
	files     []*ast.File
}

func readSource(dir string) (*source, error) {
	// Inspect source independently of the local platform's build constraints.
	//nolint:staticcheck // This is a source inventory, not a Go build.
	packages, err := parser.ParseDir(token.NewFileSet(), dir, func(info fs.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if len(packages) != 1 {
		return nil, fmt.Errorf("expected one source package in %s, found %d", dir, len(packages))
	}
	s := &source{structs: map[string]*ast.StructType{}, constants: map[string]string{}}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			s.files = append(s.files, file)
			ast.Inspect(file, func(node ast.Node) bool {
				switch item := node.(type) {
				case *ast.TypeSpec:
					if st, ok := item.Type.(*ast.StructType); ok {
						s.structs[item.Name.Name] = st
					}
				case *ast.ValueSpec:
					for i, name := range item.Names {
						if i < len(item.Values) {
							if value := s.stringValue(item.Values[i]); value != "" {
								s.constants[name.Name] = value
							}
						}
					}
				}
				return true
			})
		}
	}
	return s, nil
}

func (s *source) stringValue(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			text, _ := strconv.Unquote(value.Value)
			return text
		}
	case *ast.Ident:
		return s.constants[value.Name]
	}
	return ""
}

func (s *source) collect(keys Keys, prefix, name, tagName string, visiting map[string]bool) {
	st := s.structs[name]
	if st == nil || visiting[name] {
		return
	}
	visiting[name] = true
	defer delete(visiting, name)
	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}
		raw, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			continue
		}
		parts := strings.Split(reflect.StructTag(raw).Get(tagName), ",")
		key := parts[0]
		if key == "" || key == "-" || slices.Contains(parts[1:], "label") {
			continue
		}
		path := join(prefix, key)
		keys[path] = true
		if child := typeName(field.Type); child != "" {
			s.collect(keys, path, child, tagName, visiting)
		}
	}
}

func typeName(expr ast.Expr) string {
	switch item := expr.(type) {
	case *ast.Ident:
		return item.Name
	case *ast.StarExpr:
		return typeName(item.X)
	case *ast.ArrayType:
		return typeName(item.Elt)
	}
	return ""
}

func join(prefix, name string) string {
	return strings.Trim(prefix+"."+name, ".")
}

func firstDir(root string, candidates ...string) (string, error) {
	for _, candidate := range candidates {
		matches, err := filepath.Glob(filepath.Join(root, candidate, "*.go"))
		if err != nil {
			return "", err
		}
		if len(matches) > 0 {
			return filepath.Join(root, candidate), nil
		}
	}
	return "", fmt.Errorf("no supported source directory under %s: %v", root, candidates)
}

// Upstream returns parser tags, manually parsed stanza roots, profile fields,
// Raft configuration map reads, and retry_join JSON fields. Seal provider options
// and API payloads remain opaque; they are not upstream server struct fields.
func Upstream(root string) (Keys, error) {
	server, err := firstDir(root, "internal/command/server", "command/server")
	if err != nil {
		return nil, err
	}
	shared, err := firstDir(root, "internal/helper/configutil", "helper/configutil", "internalshared/configutil")
	if err != nil {
		return nil, err
	}
	profiles, err := firstDir(root, "internal/helper/profiles", "helper/profiles", "internalshared/profiles")
	if err != nil {
		return nil, err
	}
	raft, err := firstDir(root, "internal/physical/raft", "physical/raft")
	if err != nil {
		return nil, err
	}
	keys := Keys{}
	roots := []struct{ dir, prefix, name, tag string }{
		{server, "", "Config", "hcl"}, {shared, "", "SharedConfig", "hcl"},
		{shared, "listener", "Listener", "hcl"}, {shared, "telemetry", "Telemetry", "hcl"},
		{server, "audit", "AuditDevice", "hcl"}, {server, "plugin", "PluginConfig", "hcl"},
		{profiles, "initialize", "OuterConfig", "hcl"},
		{profiles, "initialize.request", "RequestConfig", "hcl"},
		{raft, "storage.retry_join", "LeaderJoinInfo", "json"},
	}
	cache := map[string]*source{}
	for _, root := range roots {
		s := cache[root.dir]
		if s == nil {
			s, err = readSource(root.dir)
			if err != nil {
				return nil, err
			}
			cache[root.dir] = s
		}
		// PluginConfig does not exist before the declarative plugin feature.
		if s.structs[root.name] == nil && root.name != "PluginConfig" {
			return nil, fmt.Errorf("type %s is missing from %s", root.name, root.dir)
		}
		s.collect(keys, root.prefix, root.name, root.tag, map[string]bool{})
		if root.prefix != "" && s.structs[root.name] != nil {
			keys[root.prefix] = true
		}
	}
	for _, block := range []string{"storage", "seal", "service_registration", "initialize.request.data"} {
		keys[block] = true
	}
	cache[raft].collectRaftKeys(keys)
	return keys, nil
}

func (s *source) collectRaftKeys(keys Keys) {
	for _, file := range s.files {
		ast.Inspect(file, func(node ast.Node) bool {
			index, ok := node.(*ast.IndexExpr)
			if !ok {
				return true
			}
			var name string
			switch expr := index.X.(type) {
			case *ast.Ident:
				name = expr.Name
			case *ast.SelectorExpr:
				name = expr.Sel.Name
			}
			if name == "conf" {
				if key := s.stringValue(index.Index); key != "" {
					keys[join("storage", key)] = true
				}
			}
			return true
		})
	}
}

// Operator returns all declared HCL attribute paths and manually rendered roots.
func Operator(root string) (Keys, error) {
	s, err := readSource(filepath.Join(root, "internal", "adapter", "config"))
	if err != nil {
		return nil, err
	}
	keys := Keys{}
	prefixes := map[string]string{
		"hclCoreAttributes": "", "hclUserConfigurationAttributes": "",
		"hclListenerTCP": "listener", "hclListenerTelemetry": "listener.telemetry",
		"hclStorageRaft": "storage", "hclRetryJoin": "storage.retry_join",
		"hclAuditDevice": "audit", "hclPlugin": "plugin", "hclTelemetry": "telemetry",
		"hclInitialize": "initialize", "hclInitializeRequest": "initialize.request",
	}
	for name := range s.structs {
		if strings.HasPrefix(name, "hclSeal") {
			prefixes[name] = "seal"
		}
	}
	for name, prefix := range prefixes {
		if s.structs[name] == nil {
			return nil, fmt.Errorf("operator HCL type %s is missing", name)
		}
		s.collect(keys, prefix, name, "hcl", map[string]bool{})
		if prefix != "" {
			keys[prefix] = true
		}
	}
	for _, key := range []string{"audit.options", "service_registration", "initialize", "initialize.request",
		"initialize.request.operation", "initialize.request.path", "initialize.request.data",
		"initialize.request.headers", "initialize.request.when"} {
		keys[key] = true
	}
	return keys, nil
}

// Opaque reports paths whose children are defined by a provider or target API.
func Opaque(path string) bool {
	for _, prefix := range []string{"seal.", "audit.options.", "initialize.request.data."} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
