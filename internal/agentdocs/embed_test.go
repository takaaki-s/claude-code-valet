package agentdocs

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestListParsesEveryDoc is the test load()'s panic exists for: a malformed
// frontmatter committed to docs/ fails here rather than at a user's first
// `jin docs list`.
func TestListParsesEveryDoc(t *testing.T) {
	docs := List()
	if len(docs) == 0 {
		t.Fatal("List() returned no docs; the embed pattern matched nothing")
	}

	seen := map[string]bool{}
	for _, d := range docs {
		if d.Name == "" {
			t.Error("doc with empty name")
		}
		if seen[d.Name] {
			t.Errorf("duplicate doc name %q", d.Name)
		}
		seen[d.Name] = true

		if d.Title == "" {
			t.Errorf("%s: empty title", d.Name)
		}
		if d.Description == "" {
			t.Errorf("%s: empty description", d.Name)
		}
		if strings.TrimSpace(d.Body) == "" {
			t.Errorf("%s: empty body", d.Name)
		}
		// A body that still carries its fence means the split leaked the
		// header into the text an agent reads.
		if strings.HasPrefix(d.Body, frontmatterFence) {
			t.Errorf("%s: body starts with the frontmatter fence", d.Name)
		}
	}
}

// TestListIsSortedByName pins the ordering `jin docs list` relies on, so the
// output stays diffable across versions.
func TestListIsSortedByName(t *testing.T) {
	docs := List()
	for i := 1; i < len(docs); i++ {
		if docs[i-1].Name >= docs[i].Name {
			t.Fatalf("List() not sorted by name: %q before %q", docs[i-1].Name, docs[i].Name)
		}
	}
}

// TestListDoesNotShareBackingArray guards the slices.Clone in List: a caller
// mutating the returned slice must not corrupt what every later caller sees.
func TestListDoesNotShareBackingArray(t *testing.T) {
	first := List()
	if len(first) == 0 {
		t.Fatal("no docs")
	}
	original := first[0].Name
	first[0].Name = "clobbered"

	if got := List()[0].Name; got != original {
		t.Fatalf("List() leaked its backing array: second call returned %q, want %q", got, original)
	}
}

func TestGetReturnsBody(t *testing.T) {
	docs := List()
	want := docs[0]

	got, err := Get(want.Name)
	if err != nil {
		t.Fatalf("Get(%q): %v", want.Name, err)
	}
	if got.Body != want.Body {
		t.Errorf("Get(%q) returned a different body than List()", want.Name)
	}
}

// TestGetUnknownNamesAlternatives checks the recovery path: an agent that
// guessed wrong should be able to fix itself from the error alone.
func TestGetUnknownNamesAlternatives(t *testing.T) {
	_, err := Get("no-such-doc")
	if err == nil {
		t.Fatal("Get on an unknown name returned no error")
	}
	for _, d := range List() {
		if !strings.Contains(err.Error(), d.Name) {
			t.Errorf("error does not mention available doc %q: %v", d.Name, err)
		}
	}
}

func TestParseDocRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"no opening fence", "title: x\ndescription: y\n---\nbody\n"},
		{"no closing fence", "---\ntitle: x\ndescription: y\nbody\n"},
		{"missing title", "---\ndescription: y\n---\nbody\n"},
		{"missing description", "---\ntitle: x\n---\nbody\n"},
		{"malformed yaml", "---\ntitle: [unclosed\n---\nbody\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseDoc("sample", tc.raw); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestParseDocSplitsHeaderFromBody(t *testing.T) {
	doc, err := parseDoc("sample", "---\ntitle: T\ndescription: D\n---\n\n# Heading\n\ntext\n")
	if err != nil {
		t.Fatalf("parseDoc: %v", err)
	}
	if doc.Title != "T" || doc.Description != "D" {
		t.Errorf("frontmatter not applied: %+v", doc)
	}
	if want := "# Heading\n\ntext\n"; doc.Body != want {
		t.Errorf("body = %q, want %q", doc.Body, want)
	}
}

// skillFrontmatter mirrors the fields all three agents require of a skill.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// TestSkillSatisfiesEveryAgent pins the constraints that let one file serve
// Claude Code, Codex and opencode. opencode's are the strictest, so meeting
// them meets all three; a change that breaks them would install a skill that
// silently never loads.
func TestSkillSatisfiesEveryAgent(t *testing.T) {
	raw := Skill()

	rest, ok := strings.CutPrefix(raw, frontmatterFence+"\n")
	if !ok {
		t.Fatal("skill does not open with a frontmatter fence")
	}
	header, body, ok := strings.Cut(rest, "\n"+frontmatterFence+"\n")
	if !ok {
		t.Fatal("skill has no closing frontmatter fence")
	}

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		t.Fatalf("parse skill frontmatter: %v", err)
	}

	if fm.Name != skillDirName {
		t.Errorf("skill name = %q, want %q (it must match the directory it installs into)", fm.Name, skillDirName)
	}
	// opencode: 1-64 chars, lowercase alphanumerics and hyphens only.
	if n := len(fm.Name); n < 1 || n > 64 {
		t.Errorf("skill name length %d is outside opencode's 1-64 range", n)
	}
	for _, r := range fm.Name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Errorf("skill name %q contains %q; opencode allows only lowercase alphanumerics and hyphens", fm.Name, r)
			break
		}
	}
	// opencode: 1-1024 chars.
	if n := len(fm.Description); n < 1 || n > 1024 {
		t.Errorf("skill description length %d is outside opencode's 1-1024 range", n)
	}

	if strings.TrimSpace(body) == "" {
		t.Error("skill has an empty body")
	}
}

// TestSkillStaysShort keeps the install prompt readable. The whole design
// rests on a user being able to read the file before consenting to it, so the
// limit is a feature rather than tidiness.
func TestSkillStaysShort(t *testing.T) {
	const maxLines = 30
	if n := strings.Count(strings.TrimRight(Skill(), "\n"), "\n") + 1; n > maxLines {
		t.Errorf("skill is %d lines, limit is %d — trim it rather than raising the bound", n, maxLines)
	}
}

// TestSkillAndContextPointAtDocs guards the one job both files have. Either
// one growing its own command reference is the drift this package exists to
// prevent.
func TestSkillAndContextPointAtDocs(t *testing.T) {
	for name, text := range map[string]string{"skill": Skill(), "context": Context()} {
		if !strings.Contains(text, "jin docs list") {
			t.Errorf("%s does not mention `jin docs list`", name)
		}
		if !strings.Contains(text, "jin docs show") {
			t.Errorf("%s does not mention `jin docs show`", name)
		}
	}
}

func TestContextIsNotEmpty(t *testing.T) {
	if strings.TrimSpace(Context()) == "" {
		t.Fatal("Context() is empty; child sessions would be injected with nothing")
	}
}

func TestSkillTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	claude := filepath.Join(home, ".claude", "skills", skillDirName, SkillFileName)
	agents := filepath.Join(home, ".agents", "skills", skillDirName, SkillFileName)
	opencode := filepath.Join(home, ".config", "opencode", "skills", skillDirName, SkillFileName)

	tests := []struct {
		name  string
		kinds []string
		want  []string
	}{
		{"none", nil, nil},
		{"claude only", []string{KindClaude}, []string{claude}},
		{"codex only", []string{KindCodex}, []string{agents}},
		// opencode reads ~/.claude and ~/.agents too, so it only needs a path
		// of its own when neither of the others contributed one.
		{"opencode only", []string{KindOpenCode}, []string{opencode}},
		{"claude and codex", []string{KindClaude, KindCodex}, []string{claude, agents}},
		{"all three", []string{KindClaude, KindCodex, KindOpenCode}, []string{claude, agents}},
		{"claude and opencode", []string{KindClaude, KindOpenCode}, []string{claude}},
		{"codex and opencode", []string{KindCodex, KindOpenCode}, []string{agents}},
		// Order of the input must not decide whether opencode adds a path.
		{"opencode listed first", []string{KindOpenCode, KindClaude}, []string{claude}},
		{"unknown kind ignored", []string{"antigravity"}, nil},
		{"duplicates collapse", []string{KindClaude, KindClaude}, []string{claude}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SkillTargets(tc.kinds)
			if err != nil {
				t.Fatalf("SkillTargets: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("target %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestSkillTargetsHonoursXDGConfigHome checks the opencode-only path: writing
// to a hardcoded ~/.config on a machine with XDG_CONFIG_HOME set would put the
// skill where opencode never looks, and nothing would report the miss.
func TestSkillTargetsHonoursXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := SkillTargets([]string{KindOpenCode})
	if err != nil {
		t.Fatalf("SkillTargets: %v", err)
	}
	want := filepath.Join(xdg, "opencode", "skills", skillDirName, SkillFileName)
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want [%s]", got, want)
	}
}
