package replies

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Renderer is the interface for reply template rendering. *Replies satisfies
// it; tests can substitute a mock to verify executor behavior without
// loading the real YAML file.
type Renderer interface {
	Get(category, key string, vars any) string
	Render(templateName string, data any) (string, error)
	Has(category, key string) bool
}

// Compile-time check that *Replies satisfies Renderer.
var _ Renderer = (*Replies)(nil)

type replyEntry []string

func (r *replyEntry) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*r = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, c := range node.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("reply variant must be a string or a list of strings, got %s", node.Tag)
			}
			out = append(out, c.Value)
		}
		*r = out
		return nil
	default:
		return fmt.Errorf("reply variant must be a string or a list of strings, got %s", node.Tag)
	}
}

type Replies struct {
	categories map[string]map[string][]*template.Template
}

func Load(path string) (*Replies, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read replies file: %w", err)
	}
	return parse(data)
}

func parse(data []byte) (*Replies, error) {
	var categories map[string]map[string]replyEntry
	if err := yaml.Unmarshal(data, &categories); err != nil {
		return nil, fmt.Errorf("parse replies yaml: %w", err)
	}
	out := make(map[string]map[string][]*template.Template, len(categories))
	for cat, keys := range categories {
		out[cat] = make(map[string][]*template.Template, len(keys))
		for key, entry := range keys {
			variants := make([]*template.Template, 0, len(entry))
			for i, v := range entry {
				t, err := template.New(fmt.Sprintf("%s.%s.%d", cat, key, i)).Parse(v)
				if err != nil {
					return nil, fmt.Errorf("parse reply %s.%s variant %d: %w", cat, key, i, err)
				}
				variants = append(variants, t)
			}
			out[cat][key] = variants
		}
	}
	return &Replies{categories: out}, nil
}

func (r *Replies) Get(category, key string, vars any) string {
	variants := r.lookup(category, key)
	if len(variants) == 0 {
		return fmt.Sprintf("[%s.%s]", category, key)
	}
	tmpl := variants[rand.Intn(len(variants))]
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return fmt.Sprintf("[%s.%s: render error]", category, key)
	}
	return buf.String()
}

func (r *Replies) Render(templateName string, data any) (string, error) {
	category, key, ok := splitDot(templateName)
	if !ok {
		return "", fmt.Errorf("replies: invalid template name %q", templateName)
	}

	variants := r.lookup(category, key)
	if len(variants) == 0 {
		return "", fmt.Errorf("replies: template %q not found", templateName)
	}

	tmpl := variants[rand.Intn(len(variants))]
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("replies: rendering %q: %w", templateName, err)
	}
	return buf.String(), nil
}

func (r *Replies) lookup(category, key string) []*template.Template {
	if cat, ok := r.categories[category]; ok {
		if variants, ok := cat[key]; ok {
			return variants
		}
	}
	return nil
}

func (r *Replies) Has(category, key string) bool {
	cat, ok := r.categories[category]
	if !ok {
		return false
	}
	_, ok = cat[key]
	return ok
}

func splitDot(name string) (category, key string, ok bool) {
	idx := strings.Index(name, ".")
	if idx < 0 {
		return "", "", false
	}
	return name[:idx], name[idx+1:], true
}
