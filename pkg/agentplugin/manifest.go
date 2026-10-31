package agentplugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Author is the optional plugin.json author object.
type Author struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Manifest is a validated plugin.json.
type Manifest struct {
	Schema      string
	Version     string // spec version selected by Schema, e.g. "1.0.0"
	Name        string
	PluginVer   string // the plugin's own "version" field
	Description string
	Author      *Author
	Homepage    string
	Repository  string
	License     string
	Keywords    []string
	// Extensions holds the raw value of each extensions namespace. fir
	// implements no namespace, so the contents are never validated.
	Extensions map[string]json.RawMessage
}

// decodeObject parses data as a JSON object (and nothing else).
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("must be a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return obj, nil
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// decodeString decodes raw as a JSON string (null is not a string).
func decodeString(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(t, &s) == nil
}

// decodeStringArray decodes raw as an array of strings.
func decodeStringArray(raw json.RawMessage) ([]string, bool) {
	t := bytes.TrimSpace(raw)
	var items []json.RawMessage
	if len(t) == 0 || t[0] != '[' || json.Unmarshal(t, &items) != nil {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := decodeString(it)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// decodeStringMap decodes raw as an object whose values are all strings.
func decodeStringMap(raw json.RawMessage) (map[string]string, bool) {
	var obj map[string]json.RawMessage
	if !isJSONObject(raw) || json.Unmarshal(raw, &obj) != nil {
		return nil, false
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		s, ok := decodeString(v)
		if !ok {
			return nil, false
		}
		out[k] = s
	}
	return out, true
}

// ValidPluginName reports whether name satisfies §5.5: 1-64 characters of
// a-z, 0-9, '-' and '.', starting and ending alphanumeric, with no "--" or
// "..".
func ValidPluginName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	alnum := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !alnum(c) && c != '-' && c != '.' {
			return false
		}
	}
	if !alnum(name[0]) || !alnum(name[len(name)-1]) {
		return false
	}
	return !strings.Contains(name, "--") && !strings.Contains(name, "..")
}

// ParseManifest validates plugin.json content per §5 and §8.1. A non-nil
// error means the plugin must be rejected. Diagnostics report the non-fatal
// cases: unknown top-level fields and a non-object extensions field (both
// ignored). Diagnostic paths are left empty for the caller to fill in.
func ParseManifest(data []byte) (*Manifest, []Diagnostic, error) {
	obj, err := decodeObject(data)
	if err != nil {
		return nil, nil, err
	}
	var diags []Diagnostic
	m := &Manifest{}

	rawSchema, ok := obj["$schema"]
	if !ok {
		return nil, nil, errors.New(`required field "$schema" is missing`)
	}
	schema, ok := decodeString(rawSchema)
	if !ok {
		return nil, nil, errors.New(`required field "$schema" must be a string`)
	}
	ver, ok := pluginSchemas[schema]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported Agent Plugins version: $schema %q is not recognised (fir supports %s)", schema, PluginSchemaID)
	}
	m.Schema, m.Version = schema, ver

	rawName, ok := obj["name"]
	if !ok {
		return nil, nil, errors.New(`required field "name" is missing`)
	}
	if m.Name, ok = decodeString(rawName); !ok {
		return nil, nil, errors.New(`required field "name" must be a string`)
	}
	if !ValidPluginName(m.Name) {
		return nil, nil, fmt.Errorf(`required field "name" %q is invalid: use 1-64 of a-z, 0-9, '-' and '.', start and end alphanumeric, no "--" or ".."`, m.Name)
	}

	strField := func(key string, dst *string) error {
		raw, ok := obj[key]
		if !ok {
			return nil
		}
		s, ok := decodeString(raw)
		if !ok {
			return fmt.Errorf("field %q must be a string", key)
		}
		*dst = s
		return nil
	}
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"version", &m.PluginVer},
		{"description", &m.Description},
		{"homepage", &m.Homepage},
		{"repository", &m.Repository},
		{"license", &m.License},
	} {
		if err := strField(f.key, f.dst); err != nil {
			return nil, nil, err
		}
	}
	if raw, ok := obj["keywords"]; ok {
		if m.Keywords, ok = decodeStringArray(raw); !ok {
			return nil, nil, errors.New(`field "keywords" must be an array of strings`)
		}
	}
	if raw, ok := obj["author"]; ok {
		a, err := parseAuthor(raw)
		if err != nil {
			return nil, nil, err
		}
		m.Author = a
	}
	if raw, ok := obj["extensions"]; ok {
		if !isJSONObject(raw) || json.Unmarshal(raw, &m.Extensions) != nil {
			diags = append(diags, Diagnostic{Message: `field "extensions" is not an object; ignored`})
		}
		// Each member must be an object (the schema's additionalProperties);
		// what is inside is not validated, as fir implements no namespace.
		for ns, v := range m.Extensions {
			if !isJSONObject(v) {
				return nil, nil, fmt.Errorf("extensions[%q] must be an object", ns)
			}
		}
	}

	var unknown []string
	for k := range obj {
		switch k {
		case "$schema", "name", "version", "description", "author", "homepage",
			"repository", "license", "keywords", "extensions":
		default:
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		diags = append(diags, Diagnostic{Message: fmt.Sprintf("unknown top-level field %q; ignored", k)})
	}
	return m, diags, nil
}

func parseAuthor(raw json.RawMessage) (*Author, error) {
	var obj map[string]json.RawMessage
	if !isJSONObject(raw) || json.Unmarshal(raw, &obj) != nil {
		return nil, errors.New(`field "author" must be an object`)
	}
	a := &Author{}
	for k, v := range obj {
		var dst *string
		switch k {
		case "name":
			dst = &a.Name
		case "email":
			dst = &a.Email
		case "url":
			dst = &a.URL
		default:
			return nil, fmt.Errorf(`field "author" has unknown field %q (only name, email, url are allowed)`, k)
		}
		s, ok := decodeString(v)
		if !ok {
			return nil, fmt.Errorf(`field "author.%s" must be a string`, k)
		}
		*dst = s
	}
	return a, nil
}
