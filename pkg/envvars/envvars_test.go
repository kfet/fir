package envvars

import (
	"strings"
	"testing"
)

func TestFormatHelpText(t *testing.T) {
	text := FormatHelpText()
	if !strings.Contains(text, "FIR_DEBUG") {
		t.Error("help text should contain FIR_DEBUG")
	}
	if !strings.Contains(text, "FIR_EXT_TIMEOUT") {
		t.Error("help text should contain FIR_EXT_TIMEOUT")
	}
	if !strings.Contains(text, "ANTHROPIC_API_KEY") {
		t.Error("help text should contain ANTHROPIC_API_KEY")
	}
	// Internal vars should be excluded
	if strings.Contains(text, "FIR_REEXEC_CONTINUE") {
		t.Error("help text should not contain internal var FIR_REEXEC_CONTINUE")
	}
}

func TestFormatMarkdownTable(t *testing.T) {
	table := FormatMarkdownTable()
	if !strings.Contains(table, "| Variable |") {
		t.Error("markdown table should have header")
	}
	if !strings.Contains(table, "FIR_EXT_TIMEOUT") {
		t.Error("markdown table should contain FIR_EXT_TIMEOUT")
	}
	if strings.Contains(table, "FIR_REEXEC_CONTINUE") {
		t.Error("markdown table should not contain internal var")
	}
}

func TestRegistryIsSorted(t *testing.T) {
	for i := 1; i < len(Registry); i++ {
		if Registry[i].Name < Registry[i-1].Name {
			t.Errorf("Registry not sorted: %s before %s", Registry[i-1].Name, Registry[i].Name)
		}
	}
	for i := 1; i < len(ProviderKeys); i++ {
		if ProviderKeys[i].Name < ProviderKeys[i-1].Name {
			t.Errorf("ProviderKeys not sorted: %s before %s", ProviderKeys[i-1].Name, ProviderKeys[i].Name)
		}
	}
}
