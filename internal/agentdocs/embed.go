// Package agentdocs holds the documentation jind-ai serves to the AI agents
// that drive it, plus the skill and session-start context that point an agent
// at that documentation.
//
// Everything here is embedded in the binary rather than installed alongside
// it. The point is not convenience but truthfulness: a doc that ships
// separately can describe a `jin` the user does not have, and an agent acting
// on a stale instruction fails in ways that look like the tool is broken. One
// binary, one set of docs, no drift.
//
// The split between the three embedded trees is deliberate:
//
//   - docs/    — the reference material, served by `jin docs list|show`
//   - skill/   — the ~30-line skill `jin init` offers to install, whose only
//     job is to send an agent to `jin docs list`
//   - context/ — the few lines injected into every child session at start
//
// skill/ and context/ deliberately contain no command reference of their own.
// The moment either one starts explaining selectors or flags, it becomes a
// second copy of docs/ that nothing keeps in step.
package agentdocs

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed docs/*.md
var docsFS embed.FS

//go:embed skill/SKILL.md
var skillSource string

//go:embed context/jin-agent.md
var contextSource string

// Doc is one agent-facing document: the metadata an agent needs to decide
// whether to read it, plus the body it gets when it does.
//
// Body carries no JSON tag because `jin docs list --json` exists to let an
// agent choose what to read — shipping every body in the listing would defeat
// the two-step design and flood the caller's context with material it did not
// ask for. `jin docs show` is how a body is fetched.
type Doc struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Body        string `json:"-"`
}

// frontmatter is the YAML header every embedded doc carries.
//
// Only these two fields exist, and both are required. Description does the
// real work: it is what an agent reads in `docs list` output to decide which
// doc answers its question, so a vague one costs a wasted `docs show` round
// trip. The test suite enforces that both are non-empty.
type frontmatter struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

var (
	loadOnce sync.Once
	loaded   []Doc
)

// List returns every embedded doc, ordered by name.
//
// The order is stable so that `jin docs list` output can be diffed across
// versions, and alphabetical rather than curated because a curated order is
// one more thing to keep in step with the file set.
func List() []Doc {
	loadOnce.Do(load)
	return slices.Clone(loaded)
}

// Get returns the doc with the given name.
//
// The error names every available doc. An agent that guessed wrong should be
// able to correct itself from the failure alone, without a second round trip
// through `docs list` — that round trip is exactly the kind of friction that
// makes an agent give up on a tool and start scraping `--help` instead.
func Get(name string) (Doc, error) {
	loadOnce.Do(load)
	for _, d := range loaded {
		if d.Name == name {
			return d, nil
		}
	}
	names := make([]string, 0, len(loaded))
	for _, d := range loaded {
		names = append(names, d.Name)
	}
	return Doc{}, fmt.Errorf("no such doc: %q (available: %s)", name, strings.Join(names, ", "))
}

// Skill returns the skill document `jin init` offers to install.
func Skill() string { return skillSource }

// Context returns the text injected into a child session at start.
//
// The same string reaches all three adapters — Claude Code and Codex through
// their SessionStart hook's additionalContext, opencode through a file named
// in its config's instructions list. Writing it once is the point: three
// per-kind wordings would be three things to keep in step, which is the
// problem this whole package exists to avoid.
func Context() string { return contextSource }

// load parses every embedded doc. Called once, through loadOnce.
//
// A parse failure panics rather than returning an error. The input is
// compiled into the binary, so a failure here means a malformed doc was
// committed — not something a user did, and not something a caller could
// handle. TestAllDocsParse turns that panic into a build-time failure, which
// is where it belongs; degrading to "this doc silently disappeared from the
// listing" would ship the bug instead.
func load() {
	entries, err := docsFS.ReadDir("docs")
	if err != nil {
		panic(fmt.Sprintf("agentdocs: read embedded docs: %v", err))
	}

	loaded = make([]Doc, 0, len(entries))
	for _, e := range entries {
		// No filtering on name or type: the //go:embed pattern above admits
		// only top-level .md files, so anything reached here is a doc.
		//
		// path.Join, not filepath.Join — embed.FS paths are always
		// slash-separated regardless of the host OS.
		raw, err := docsFS.ReadFile(path.Join("docs", e.Name()))
		if err != nil {
			panic(fmt.Sprintf("agentdocs: read %s: %v", e.Name(), err))
		}
		doc, err := parseDoc(strings.TrimSuffix(e.Name(), ".md"), string(raw))
		if err != nil {
			panic(fmt.Sprintf("agentdocs: %v", err))
		}
		loaded = append(loaded, doc)
	}
	slices.SortFunc(loaded, func(a, b Doc) int { return strings.Compare(a.Name, b.Name) })
}

// frontmatterFence is the delimiter line bounding a doc's YAML header.
const frontmatterFence = "---"

// parseDoc splits a doc into its YAML frontmatter and body.
//
// The shape is fixed and unforgiving: the file opens with a fence line, the
// next fence closes the header, and everything after is the body. Anything
// else is an error rather than a best-effort recovery, because every input is
// a file in this repository and a lenient parser would let a typo through as
// an empty description.
func parseDoc(name, raw string) (Doc, error) {
	rest, ok := strings.CutPrefix(raw, frontmatterFence+"\n")
	if !ok {
		return Doc{}, fmt.Errorf("%s: missing opening %q fence", name, frontmatterFence)
	}
	header, body, ok := strings.Cut(rest, "\n"+frontmatterFence+"\n")
	if !ok {
		return Doc{}, fmt.Errorf("%s: missing closing %q fence", name, frontmatterFence)
	}

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		return Doc{}, fmt.Errorf("%s: parse frontmatter: %w", name, err)
	}
	if fm.Title == "" {
		return Doc{}, fmt.Errorf("%s: frontmatter has no title", name)
	}
	if fm.Description == "" {
		return Doc{}, fmt.Errorf("%s: frontmatter has no description", name)
	}

	return Doc{
		Name:        name,
		Title:       fm.Title,
		Description: fm.Description,
		Body:        strings.TrimLeft(body, "\n"),
	}, nil
}

// skillDirName is the directory a skill lives in across all three agents, and
// therefore the name an agent invokes it by.
const skillDirName = "jind-ai"

// SkillFileName is the file every agent looks for inside a skill directory.
//
// The all-caps spelling is not a style choice: opencode requires it, and
// Claude Code and Codex accept it, so this is the one spelling that works for
// all three.
const SkillFileName = "SKILL.md"

// Agent kinds SkillTargets knows a skill location for.
//
// These mirror the adapter kinds registered in internal/agent/register but are
// spelled out rather than imported, because they answer a different question:
// not "which adapters was jin built with" but "for which agents do we know
// where a skill has to live". The two sets can legitimately differ — a newly
// registered adapter has no skill location until someone works one out — and
// SkillTargets ignores kinds it does not recognise for exactly that reason.
//
// Callers that need the list should use SkillKinds rather than restating it.
const (
	KindClaude   = "claude"
	KindCodex    = "codex"
	KindOpenCode = "opencode"
)

// SkillKinds returns the kinds SkillTargets understands, in the order they
// should be probed and reported. It is the single list: a caller that wants to
// search PATH, or to name the supported agents in a message, derives both from
// here instead of hand-maintaining a second copy that can drift silently.
func SkillKinds() []string {
	return []string{KindClaude, KindCodex, KindOpenCode}
}

// HookContextFlag is the flag `jin hook` exposes to print the agent-facing
// context, and that adapters put on their SessionStart hook command.
//
// HookCommand below is the only thing that should ever spell it into a command
// line. It is exported so the CLI can register the flag under the same name;
// those two uses are the whole surface.
const HookContextFlag = "emit-context"

// HookCommand returns the command an adapter should configure as its hook
// handler, optionally asking jin to print the agent-facing context.
//
// This exists because the flag name is otherwise a three-way coupling with no
// compiler between the parts: the CLI registers it, and the Claude Code and
// Codex adapters each embed it into a command string. Renaming it in one place
// would leave the adapters emitting a flag cobra rejects, which fails the hook
// outright — so SessionStart status detection would break, not merely the
// context injection — while each adapter's test kept asserting its own copy
// and stayed green. Loud in production, silent in CI.
//
// execPath is used verbatim. Callers whose command string is destined for a
// quoted context (the Codex adapter's TOML-inside-shell values) must escape it
// before calling; the escape rules belong to that adapter, not here.
func HookCommand(execPath string, emitContext bool) string {
	if emitContext {
		return execPath + " hook --" + HookContextFlag
	}
	return execPath + " hook"
}

// SkillTargets returns the absolute SKILL.md paths that make the skill visible
// to the given agent kinds, with no duplicates.
//
// The mapping exploits an overlap in where the three agents look:
//
//	claude    ~/.claude/skills/<name>/SKILL.md          (and nowhere else)
//	codex     ~/.agents/skills/<name>/SKILL.md
//	opencode  ~/.config/opencode/skills/, ~/.claude/skills/, ~/.agents/skills/
//
// Because opencode reads both of the other two locations, it needs a path of
// its own only when neither claude nor codex contributed one. So the common
// case — a machine with Claude Code and Codex installed — is two files, and
// adding opencode to that machine costs nothing.
//
// Kinds this package does not recognise are ignored rather than rejected: a
// newly registered adapter should not break `jin init` before anyone has
// worked out where its skills live.
func SkillTargets(kinds []string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("agentdocs: resolve home directory: %w", err)
	}

	var targets []string
	add := func(dir string) {
		p := filepath.Join(dir, skillDirName, SkillFileName)
		if !slices.Contains(targets, p) {
			targets = append(targets, p)
		}
	}

	// claude and codex first: whether opencode needs its own path depends on
	// what these two contributed, so the loop order of `kinds` must not
	// decide the outcome.
	for _, k := range kinds {
		switch k {
		case KindClaude:
			add(filepath.Join(home, ".claude", "skills"))
		case KindCodex:
			add(filepath.Join(home, ".agents", "skills"))
		}
	}
	if slices.Contains(kinds, KindOpenCode) && len(targets) == 0 {
		add(filepath.Join(openCodeConfigDir(home), "skills"))
	}

	return targets, nil
}

// openCodeConfigDir resolves opencode's global config directory.
//
// opencode documents this as ~/.config/opencode but resolves it through the
// XDG base directory spec, so an XDG_CONFIG_HOME set by the user has to win —
// writing to a hardcoded ~/.config on such a machine would put the skill
// somewhere opencode never looks, and the failure would be silent.
func openCodeConfigDir(home string) string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}
