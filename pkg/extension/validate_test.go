package extension

import "testing"

func TestValidateExtensionName_Empty(t *testing.T) {
	name, err := ValidateExtensionName("", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if name != "fallback" {
		t.Fatalf("expected 'fallback', got %q", name)
	}
}

func TestValidateExtensionName_Valid(t *testing.T) {
	name, err := ValidateExtensionName("my-ext", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-ext" {
		t.Fatalf("expected 'my-ext', got %q", name)
	}
}

func TestValidateExtensionName_PathSeparator(t *testing.T) {
	_, err := ValidateExtensionName("../evil", "fb")
	if err == nil {
		t.Fatal("expected error for path separator")
	}
	_, err = ValidateExtensionName(`foo\bar`, "fb")
	if err == nil {
		t.Fatal("expected error for backslash")
	}
}

func TestValidateExtensionName_ControlChars(t *testing.T) {
	_, err := ValidateExtensionName("bad\x00name", "fb")
	if err == nil {
		t.Fatal("expected error for control char")
	}
	_, err = ValidateExtensionName("bad\nname", "fb")
	if err == nil {
		t.Fatal("expected error for newline")
	}
}
