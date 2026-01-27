package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Template represents a prompt template
type Template struct {
	Name    string
	Path    string
	Content string
}

// Manager manages prompt templates
type Manager struct {
	promptDir string
}

// NewManager creates a new prompt manager
func NewManager(dataDir string) *Manager {
	return &Manager{
		promptDir: filepath.Join(dataDir, "prompts"),
	}
}

// EnsureDir ensures the prompts directory exists
func (m *Manager) EnsureDir() error {
	return os.MkdirAll(m.promptDir, 0755)
}

// List returns all available prompt templates
func (m *Manager) List() ([]Template, error) {
	if err := m.EnsureDir(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(m.promptDir)
	if err != nil {
		return nil, err
	}

	var templates []Template
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		templateName := strings.TrimSuffix(name, ".md")
		templates = append(templates, Template{
			Name: templateName,
			Path: filepath.Join(m.promptDir, name),
		})
	}

	return templates, nil
}

// Get returns a prompt template by name
func (m *Manager) Get(name string) (*Template, error) {
	path := filepath.Join(m.promptDir, name+".md")

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("prompt template '%s' not found", name)
		}
		return nil, err
	}

	return &Template{
		Name:    name,
		Path:    path,
		Content: string(content),
	}, nil
}

// Variables holds the values for template variable expansion
type Variables struct {
	Args       string // User-provided arguments
	Branch     string // Branch name
	Repository string // Repository name
	Session    string // Session name
	WorkDir    string // Work directory path
	BaseBranch string // Base branch name
}

// Expand expands variables in a template content
func Expand(content string, vars Variables) string {
	replacements := map[string]string{
		"${args}":        vars.Args,
		"${branch}":      vars.Branch,
		"${repository}":  vars.Repository,
		"${session}":     vars.Session,
		"${workdir}":     vars.WorkDir,
		"${base_branch}": vars.BaseBranch,
	}

	result := content
	for placeholder, value := range replacements {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}

// GetExpanded gets a template and expands its variables
func (m *Manager) GetExpanded(name string, vars Variables) (string, error) {
	template, err := m.Get(name)
	if err != nil {
		return "", err
	}

	return Expand(template.Content, vars), nil
}
