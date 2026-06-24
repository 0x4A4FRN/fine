package replies

import (
	"strings"
	"testing"
)

const testYAML = `
ping:
  pong: "Pong!"
  slow:
    - "Slow…"
    - "Really slow."
greeting:
  hello: "Hello, {{.Name}}!"
`

func mustParse(t *testing.T, data string) *Replies {
	t.Helper()
	r, err := parse([]byte(data))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return r
}

func TestParse_ValidYAML(t *testing.T) {
	r := mustParse(t, testYAML)
	if !r.Has("ping", "pong") {
		t.Error("expected ping.pong to exist")
	}
	if !r.Has("ping", "slow") {
		t.Error("expected ping.slow to exist")
	}
	if !r.Has("greeting", "hello") {
		t.Error("expected greeting.hello to exist")
	}
}

func TestParse_InvalidYAML_ReturnsError(t *testing.T) {
	if _, err := parse([]byte("{{{totally: [invalid")); err == nil {
		t.Fatal("expected parse error for invalid YAML")
	}
}

func TestParse_InvalidTemplate_ReturnsError(t *testing.T) {
	bad := "ping:\n  broken: \"{{.Unclosed\"\n"
	if _, err := parse([]byte(bad)); err == nil {
		t.Fatal("expected parse error for invalid Go template")
	}
}

func TestParse_EmptyYAML_ReturnsEmptyReplies(t *testing.T) {
	r := mustParse(t, "")
	if r.Has("anything", "here") {
		t.Fatal("expected empty Replies for empty YAML")
	}
}

func TestParse_SingleStringVariant(t *testing.T) {
	yaml := "cat:\n  key: \"single\"\n"
	r := mustParse(t, yaml)
	got := r.Get("cat", "key", nil)
	if got != "single" {
		t.Fatalf("expected 'single', got %q", got)
	}
}

func TestParse_MultiStringVariant(t *testing.T) {
	yaml := "cat:\n  key:\n    - \"a\"\n    - \"b\"\n"
	r := mustParse(t, yaml)
	got := r.Get("cat", "key", nil)
	if got != "a" && got != "b" {
		t.Fatalf("expected 'a' or 'b', got %q", got)
	}
}

func TestGet_ExistingKey(t *testing.T) {
	r := mustParse(t, testYAML)
	got := r.Get("ping", "pong", nil)
	if got != "Pong!" {
		t.Fatalf("expected 'Pong!', got %q", got)
	}
}

func TestGet_MissingCategory_ReturnsPlaceholder(t *testing.T) {
	r := mustParse(t, testYAML)
	got := r.Get("missing_cat", "pong", nil)
	if !strings.Contains(got, "missing_cat.pong") {
		t.Fatalf("expected placeholder containing key path, got %q", got)
	}
}

func TestGet_MissingKey_ReturnsPlaceholder(t *testing.T) {
	r := mustParse(t, testYAML)
	got := r.Get("ping", "missing_key", nil)
	if !strings.Contains(got, "ping.missing_key") {
		t.Fatalf("expected placeholder containing key path, got %q", got)
	}
}

func TestGet_TemplateVarsSubstituted(t *testing.T) {
	r := mustParse(t, testYAML)
	got := r.Get("greeting", "hello", map[string]string{"Name": "Alice"})
	if got != "Hello, Alice!" {
		t.Fatalf("expected 'Hello, Alice!', got %q", got)
	}
}

func TestGet_MultiVariant_ReturnsOneValidOption(t *testing.T) {
	r := mustParse(t, testYAML)
	valid := map[string]bool{"Slow…": true, "Really slow.": true}

	for i := 0; i < 20; i++ {
		got := r.Get("ping", "slow", nil)
		if !valid[got] {
			t.Fatalf("unexpected variant: %q", got)
		}
	}
}

func TestGet_RenderError_ReturnsPlaceholder(t *testing.T) {

	yaml := "cat:\n  key: \"{{call .Fn}}\"\n"
	r := mustParse(t, yaml)
	type data struct{ Fn func() string }
	got := r.Get("cat", "key", data{Fn: nil})
	if !strings.Contains(got, "cat.key") {
		t.Fatalf("expected render-error placeholder containing 'cat.key', got %q", got)
	}
	if !strings.Contains(got, "render error") {
		t.Fatalf("expected 'render error' in placeholder, got %q", got)
	}
}

func TestRender_ValidTemplateName(t *testing.T) {
	r := mustParse(t, testYAML)
	got, err := r.Render("ping.pong", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Pong!" {
		t.Fatalf("expected 'Pong!', got %q", got)
	}
}

func TestRender_WithVars(t *testing.T) {
	r := mustParse(t, testYAML)
	got, err := r.Render("greeting.hello", map[string]string{"Name": "Bob"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Hello, Bob!" {
		t.Fatalf("expected 'Hello, Bob!', got %q", got)
	}
}

func TestRender_MissingTemplate_ReturnsError(t *testing.T) {
	r := mustParse(t, testYAML)
	if _, err := r.Render("ping.nonexistent", nil); err == nil {
		t.Fatal("expected error for missing template key")
	}
}

func TestRender_MissingCategory_ReturnsError(t *testing.T) {
	r := mustParse(t, testYAML)
	if _, err := r.Render("ghost.pong", nil); err == nil {
		t.Fatal("expected error for missing category")
	}
}

func TestRender_InvalidName_NoDot_ReturnsError(t *testing.T) {
	r := mustParse(t, testYAML)
	if _, err := r.Render("nodot", nil); err == nil {
		t.Fatal("expected error for template name without dot")
	}
}

func TestHas_ExistingKey(t *testing.T) {
	r := mustParse(t, testYAML)
	if !r.Has("ping", "pong") {
		t.Fatal("expected true for existing key")
	}
}

func TestHas_MissingCategory(t *testing.T) {
	r := mustParse(t, testYAML)
	if r.Has("ghost", "pong") {
		t.Fatal("expected false for missing category")
	}
}

func TestHas_MissingKey(t *testing.T) {
	r := mustParse(t, testYAML)
	if r.Has("ping", "ghost") {
		t.Fatal("expected false for missing key within existing category")
	}
}

func TestSplitDot_Valid(t *testing.T) {
	cat, key, ok := splitDot("foo.bar")
	if !ok || cat != "foo" || key != "bar" {
		t.Fatalf("got cat=%q key=%q ok=%v", cat, key, ok)
	}
}

func TestSplitDot_NoDot_NotOK(t *testing.T) {
	if _, _, ok := splitDot("nodot"); ok {
		t.Fatal("expected ok=false for string without dot")
	}
}

func TestSplitDot_MultipleDots_SplitsOnFirst(t *testing.T) {
	cat, key, ok := splitDot("foo.bar.baz")
	if !ok || cat != "foo" || key != "bar.baz" {
		t.Fatalf("got cat=%q key=%q ok=%v", cat, key, ok)
	}
}

func TestSplitDot_EmptyString_NotOK(t *testing.T) {
	if _, _, ok := splitDot(""); ok {
		t.Fatal("expected ok=false for empty string")
	}
}
