package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// config_contract.go is embedded verbatim and served to users as the reference
// for .fir/mcp.json (see contract_source.go). That only stays honest if the
// file contains the contract and nothing else — a helper function or an
// internal var dropped in there would be shipped as user documentation.
//
// These tests enforce that structurally, which is why the old prose-mirroring
// guard in pkg/resources could go: field-level drift is now impossible by
// construction, so the only thing left worth guarding is the boundary of the
// file itself.
const contractFile = "config_contract.go"

func parseContract(t *testing.T) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), contractFile, nil, parser.ParseComments)
	require.NoError(t, err)
	return f
}

// TestContractFileDeclaresOnlyTheContract is the invariant that makes
// embedding safe: every top-level declaration is an exported type or const,
// and nothing else lives in the file.
func TestContractFileDeclaresOnlyTheContract(t *testing.T) {
	f := parseContract(t)

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			t.Errorf("%s declares func %q; functions and methods belong in config.go, "+
				"not in the embedded user contract", contractFile, d.Name.Name)
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE, token.CONST:
				for _, spec := range d.Specs {
					for _, name := range declaredNames(spec) {
						if !ast.IsExported(name) {
							t.Errorf("%s declares unexported identifier %q; the contract file "+
								"must contain only the exported user-facing config surface",
								contractFile, name)
						}
					}
				}
			case token.IMPORT:
				t.Errorf("%s imports packages; the contract must be self-contained", contractFile)
			default:
				t.Errorf("%s contains a %s declaration; only exported types and consts belong here",
					contractFile, d.Tok)
			}
		default:
			t.Errorf("%s contains an unexpected declaration %T", contractFile, decl)
		}
	}
}

// TestContractStructFieldsAreJSONTagged checks the other half of "user
// contract": every field a user could set is a real, named JSON key. An
// untagged exported field would document a key that does not match what
// encoding/json actually accepts in lower_snake_case configs.
func TestContractStructFieldsAreJSONTagged(t *testing.T) {
	f := parseContract(t)

	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if field.Tag == nil || !strings.Contains(field.Tag.Value, "json:\"") {
				name := "<embedded>"
				if len(field.Names) > 0 {
					name = field.Names[0].Name
				}
				t.Errorf("%s: field %q has no json tag", contractFile, name)
			}
		}
		return true
	})
}

// TestConfigContractSourceIsTheFile guards the embed itself: what we serve
// must be the byte-for-byte contents of the file the tests above validate.
func TestConfigContractSourceIsTheFile(t *testing.T) {
	onDisk, err := os.ReadFile(contractFile)
	require.NoError(t, err)
	require.Equal(t, string(onDisk), ConfigContractSource)

	// Sanity: the embedded text really is the config surface, not an empty
	// string from a mis-pointed embed directive.
	for _, want := range []string{"type ServerConfig struct", "type AuthConfig struct", "type ConfigFile struct"} {
		require.Contains(t, ConfigContractSource, want)
	}
}

// declaredNames returns the identifiers introduced by a type or value spec.
func declaredNames(spec ast.Spec) []string {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return []string{s.Name.Name}
	case *ast.ValueSpec:
		names := make([]string, 0, len(s.Names))
		for _, n := range s.Names {
			names = append(names, n.Name)
		}
		return names
	}
	return nil
}

// TestContractFileIsComplete is the other half of the boundary: the AST tests
// above prove the file holds nothing extra, this proves it holds everything.
// Moving a type back into config.go would otherwise silently amputate the
// served reference while every other test still passed.
func TestContractFileIsComplete(t *testing.T) {
	declared := map[string]bool{}
	for _, decl := range parseContract(t).Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			for _, name := range declaredNames(spec) {
				declared[name] = true
			}
		}
	}

	pkgPath := reflect.TypeOf(ConfigFile{}).PkgPath()
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		if typ.PkgPath() == pkgPath && typ.Name() != "" && !declared[typ.Name()] {
			t.Errorf("type %s is reachable from ConfigFile but is not declared in %s; "+
				"the embedded reference would omit it", typ.Name(), contractFile)
		}
		switch typ.Kind() {
		case reflect.Map:
			walk(typ.Key())
			walk(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				walk(typ.Field(i).Type)
			}
		}
	}
	walk(reflect.TypeOf(ConfigFile{}))

	require.True(t, seen[reflect.TypeOf(AuthConfig{})], "reflection walk never reached AuthConfig — it is broken")
	require.True(t, seen[reflect.TypeOf(AuthMode(""))], "reflection walk never reached AuthMode — it is broken")
}
