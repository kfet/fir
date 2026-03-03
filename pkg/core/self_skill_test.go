package core

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestSelfSkillSettingsExample extracts the settings.json example from the
// self skill and verifies it parses into a valid Settings struct. This catches
// typos, stale field names, and malformed JSON in the documentation.
func TestSelfSkillSettingsExample(t *testing.T) {
	data, err := os.ReadFile(selfSkillPath(t))
	if err != nil {
		t.Fatalf("reading self skill: %v", err)
	}
	content := string(data)

	// Extract the jsonc code block.
	const startMarker = "```jsonc"
	const endMarker = "```"
	start := strings.Index(content, startMarker)
	if start < 0 {
		t.Fatal("no ```jsonc block found in self skill")
	}
	start += len(startMarker)
	end := strings.Index(content[start:], endMarker)
	if end < 0 {
		t.Fatal("unterminated ```jsonc block in self skill")
	}
	jsonc := content[start : start+end]

	// Strip // comments to get valid JSON.
	var cleanLines []string
	for _, line := range strings.Split(jsonc, "\n") {
		stripped := stripLineComment(line)
		cleanLines = append(cleanLines, stripped)
	}
	clean := strings.Join(cleanLines, "\n")

	var s Settings
	if err := json.Unmarshal([]byte(clean), &s); err != nil {
		t.Fatalf("settings.json example in self skill does not parse:\n%v\n\nCleaned JSON:\n%s", err, clean)
	}
}

// stripLineComment removes a trailing // comment from a JSON line,
// being careful not to strip // inside quoted strings.
func stripLineComment(line string) string {
	inString := false
	escaped := false
	for i, ch := range line {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString && ch == '/' && i+1 < len(line) && line[i+1] == '/' {
			return line[:i]
		}
	}
	return line
}

// selfSkillPath returns the file path to the builtin "self" skill.
func selfSkillPath(t *testing.T) string {
	t.Helper()
	result := LoadBuiltinSkills()
	for _, s := range result.Skills {
		if s.Name == "self" {
			return s.FilePath
		}
	}
	t.Fatal("builtin skill 'self' not found")
	return ""
}

// TestSelfSkillDocumentsAllSettings uses reflection on the Settings struct to
// verify that every JSON field name appears in the "self" builtin skill.
// If you add a new field to Settings, this test will fail until you document
// it in .fir/skills/self/SKILL.md.
func TestSelfSkillDocumentsAllSettings(t *testing.T) {
	data, err := os.ReadFile(selfSkillPath(t))
	if err != nil {
		t.Fatalf("reading self skill: %v", err)
	}
	skillContent := string(data)

	// Extract all JSON field names from Settings and nested structs.
	missing := collectJSONFields(reflect.TypeOf(Settings{}), skillContent)
	if len(missing) > 0 {
		t.Errorf("settings.json fields not documented in self skill SKILL.md:\n  %s\n\nAdd them to the settings.json Reference section.",
			strings.Join(missing, "\n  "))
	}
}

// collectJSONFields recursively collects JSON tag names from a struct type
// and returns those not found in content.
func collectJSONFields(t reflect.Type, content string) []string {
	var missing []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		if !strings.Contains(content, `"`+name+`"`) {
			missing = append(missing, name)
		}
		// Recurse into nested structs (handling pointer types).
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			missing = append(missing, collectJSONFields(ft, content)...)
		}
	}
	return missing
}
