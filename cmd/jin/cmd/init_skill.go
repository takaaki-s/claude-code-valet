package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/atomicfile"
	"golang.org/x/term"
)

// initStdinIsTerminal reports whether we can ask the user a question. It is a
// variable so tests can exercise the interactive branch, which otherwise never
// runs under `go test` (stdin is a pipe).
var initStdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// detectInstalledKinds returns the agent kinds whose executable is on PATH.
//
// Installing a skill for an agent the user does not have would create a
// directory they never asked for, in their home, to serve a tool that is not
// there. Probing PATH keeps the offer honest — and makes the prompt say
// "the agents you actually use" rather than "everything jin knows about".
//
// The set probed comes from agentdocs.SkillKinds rather than a list held here:
// there is no point probing for an agent whose skill location is unknown, and
// a second hand-maintained list would drift without anything failing.
//
// Kind name == executable name holds for all three agents today. If that ever
// stops being true, the mapping belongs to the adapter, not here.
func detectInstalledKinds() []string {
	var kinds []string
	for _, k := range agentdocs.SkillKinds() {
		if _, err := exec.LookPath(k); err == nil {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// skillPlan is what the skill step intends to do, resolved before anything is
// written or asked.
type skillPlan struct {
	// write is the set of paths that do not exist yet.
	write []string
	// existing is the set that does, and would need --force.
	existing []string
	// kinds is what was detected on PATH, for the explanatory line.
	kinds []string
}

// resolveSkillPlan works out where the skill would go and what is already
// there.
//
// --skill-dir overrides detection entirely: someone who names a directory has
// already decided, and second-guessing it against PATH would make the flag
// useless for the project-local case it exists to serve.
func resolveSkillPlan(skillDir string, force bool) (skillPlan, error) {
	var plan skillPlan
	var targets []string

	if skillDir != "" {
		targets = []string{filepath.Join(skillDir, agentdocs.SkillFileName)}
	} else {
		plan.kinds = detectInstalledKinds()
		var err error
		targets, err = agentdocs.SkillTargets(plan.kinds)
		if err != nil {
			return skillPlan{}, err
		}
	}

	for _, t := range targets {
		if fileExists(t) && !force {
			plan.existing = append(plan.existing, t)
			continue
		}
		plan.write = append(plan.write, t)
	}
	return plan, nil
}

// runSkillStep is the whole skill half of `jin init`.
//
// It never returns an error for "did not install": a user declining, or an
// environment with no agent on PATH, is a normal outcome of `jin init` and
// must not make the command look like it failed. Only a genuine write failure
// propagates.
func runSkillStep(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	// --skill / --no-skill exclusion is declared on the command itself via
	// MarkFlagsMutuallyExclusive, so cobra rejects the pair before RunE.
	if initNoSkill {
		return nil
	}

	// --force reaches the skill as well as config.yaml, but it only decides
	// whether an existing file *may* be replaced — the consent gate below is
	// unaffected, so `--force` alone in a script still writes nothing.
	plan, err := resolveSkillPlan(initSkillDir, forceInit)
	if err != nil {
		return err
	}

	if len(plan.write) == 0 && len(plan.existing) == 0 {
		// The list is derived, not spelled out: a hardcoded one would go stale
		// the moment a fourth agent gained a skill location, with nothing to
		// catch it.
		fmt.Fprintf(out, "\nNo agent CLI found on PATH (%s) — skipping the jin skill.\n",
			strings.Join(agentdocs.SkillKinds(), ", "))
		fmt.Fprintln(out, "Install one and re-run 'jin init', or use --skill-dir to choose a location yourself.")
		return nil
	}

	if len(plan.existing) > 0 {
		fmt.Fprintln(out)
		for _, p := range plan.existing {
			fmt.Fprintf(out, "Skill already present, leaving it alone: %s\n", p)
		}
	}
	// Only the missing ones are offered. Reporting "already present" and then
	// still writing the others is the behaviour a partially-installed machine
	// needs; aborting the whole step because one path existed would leave the
	// second agent permanently without the skill.
	if len(plan.write) == 0 {
		fmt.Fprintln(out, "Re-run with --force to overwrite.")
		return nil
	}

	// Checked before the proposal is printed, not after: a run that cannot ask
	// has no use for thirty lines of skill text, and `jin init` inside a setup
	// script or CI job would bury its own output under it. --skill and
	// --dry-run are excluded because for them the text is the point.
	if !initSkill && !initDryRun && !initStdinIsTerminal() {
		fmt.Fprintln(out, "\nNot a terminal — skipping the jin skill. Pass --skill to install without being asked.")
		return nil
	}

	printSkillProposal(out, plan)

	// --dry-run wins over every other mode, so it is checked before them
	// rather than as one arm among equals: `--skill --dry-run` must still
	// write nothing, and expressing that inside the switch below took the
	// same message in two places.
	if initDryRun {
		fmt.Fprintln(out, "\n--dry-run: nothing was written.")
		return nil
	}

	// Explicit opt-in (--skill) skips the question; anything else has to ask.
	if !initSkill && !confirmYN(cmd, "\nInstall this skill? [y/N]: ") {
		fmt.Fprintln(out, "Skipped. Run 'jin init --skill' later to install it.")
		return nil
	}

	home, homeErr := os.UserHomeDir()
	for _, p := range plan.write {
		// Directory components are checked here rather than inside the write:
		// neither O_EXCL nor a rename can see anything but the last element,
		// so a link at `~/.claude/skills` would redirect the whole write while
		// the consent screen named the original path.
		if homeErr == nil {
			if err := checkSkillPathUnderHome(home, p); err != nil {
				if errors.Is(err, errSkillPathUnsafe) {
					fmt.Fprintf(out, "Refusing to write %s: %v.\n", p, err)
					fmt.Fprintln(out, "Resolve the symlink, or pass --skill-dir to choose a location explicitly.")
					continue
				}
				return err
			}
		}

		// A target that filled up between planning and here is reported and
		// skipped, never overwritten: the non-overwrite promise has to hold at
		// write time, not merely at the moment the plan was drawn up.
		switch err := writeSkillFile(p, forceInit); {
		case errors.Is(err, errSkillExists):
			fmt.Fprintf(out, "Something is already at %s (it appeared since planning, or the path is a symlink) — leaving it alone.\n", p)
			fmt.Fprintln(out, "Re-run with --force to replace it.")
		case err != nil:
			return err
		default:
			fmt.Fprintf(out, "Created: %s\n", p)
		}
	}
	return nil
}

// printSkillProposal shows the full text and every destination before anything
// is written.
//
// The whole design rests on this: a file installed into the user's home is
// only reasonable to accept if they can see exactly what it says first. That
// is also why the skill is kept under thirty lines — a proposal nobody reads
// is not consent.
func printSkillProposal(w io.Writer, plan skillPlan) {
	// No trailing-newline guard: the skill is embedded from this repository
	// and TestSkillStaysShort / TestSkillSatisfiesEveryAgent already fail if
	// it is empty, so a guard here could never fire.
	fmt.Fprintf(w, "\n=== %s ===\n", agentdocs.SkillFileName)
	fmt.Fprint(w, agentdocs.Skill())
	fmt.Fprintln(w, "=== end ===")

	if len(plan.kinds) > 0 {
		fmt.Fprintf(w, "\nAgents found on PATH: %s\n", strings.Join(plan.kinds, ", "))
	}
	fmt.Fprintln(w, "This teaches your agents to drive jin. It will be written to:")
	for _, p := range plan.write {
		fmt.Fprintf(w, "  %s\n", p)
	}
}

// errSkillExists reports that the target was already occupied at write time,
// even though planning found it free.
var errSkillExists = errors.New("skill file already exists")

// skillTmpPattern names the in-flight temp file for the --force rewrite. The
// three agents all look for SKILL.md by exact name, so a dotted .tmp sibling
// is invisible to them; a crash leaves it behind as inert litter rather than a
// half-written skill.
const skillTmpPattern = ".jin-skill-*.tmp"

// writeSkillFile creates the skill's directory and writes it.
//
// The directory is created here, after consent, and never during planning: a
// `jin init` the user declined must leave no trace, and an empty
// ~/.agents/skills/jind-ai/ left behind by a refused prompt is exactly the
// kind of residue that makes people distrust a tool's install step.
//
// This is the one place in jin that writes into a directory the user owns, so
// the write has to land exactly where the consent screen said it would. Two
// distinct ways it could not:
//
//   - A symlink at the path, or at any directory above it. os.WriteFile and
//     MkdirAll both follow links, so the bytes would go somewhere the user
//     never saw while the "Created: ..." line still named the agreed path —
//     which invalidates the consent rather than merely misplacing a file.
//     Planting such a link is within reach: jin exists to run semi-autonomous
//     agents under the user's own account, so "something in $HOME made a
//     symlink" is routine, not exotic.
//   - The prompt itself. Planning and writing are separated by a wait with no
//     bound, and a file appearing in that window must not be overwritten
//     without --force (B-4).
//
// So the guard has two halves, because one flag cannot cover both:
//
//   - The final element is handled by the open mode. Without --force, O_EXCL
//     fails on anything already there, symlink included, and does so
//     atomically — no window between checking and creating. With --force,
//     atomicfile.Write publishes through a rename, which replaces a symlink
//     rather than following it, and never leaves the path empty on a crash the
//     way unlink-then-create would.
//   - Directory components are checked by the caller, before we are reached.
//     Neither open mode can see them: O_EXCL and rename both judge the last
//     element only.
//
// (O_NOFOLLOW would state the final-element rule more directly, but it is not
// reachable through the os package without build tags.)
func writeSkillFile(path string, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}

	if force {
		// Rename-based publish, per docs/conventions.md: it is symlink-safe
		// here and closes the crash window that removing first would open.
		if err := atomicfile.Write(path, []byte(agentdocs.Skill()), 0o644, skillTmpPattern); err != nil {
			return fmt.Errorf("failed to write skill: %w", err)
		}
		return nil
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errSkillExists
		}
		return fmt.Errorf("failed to write skill: %w", err)
	}
	if _, err := f.Write([]byte(agentdocs.Skill())); err != nil {
		f.Close()
		return fmt.Errorf("failed to write skill: %w", err)
	}
	// Checked Close, per the durability rule in docs/conventions.md.
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to write skill: %w", err)
	}
	return nil
}

// errSkillPathUnsafe reports a symlinked directory component on the way to a
// skill target.
var errSkillPathUnsafe = errors.New("skill path passes through a symlink")

// checkSkillPathUnderHome rejects a target whose directory components below
// home include a symlink.
//
// O_EXCL and rename each judge only the final element, so a link one level up
// — `~/.claude/skills` pointing elsewhere — still redirects the whole write
// while the consent screen names the original path. Walking the components is
// the only way to see that.
//
// home itself is not examined. A symlinked home directory is a normal, chosen
// arrangement (and the standard one on some systems), not something planted
// between planning and writing; refusing it would break real setups to defend
// against nothing. Everything the user did not explicitly choose — every
// component below home — is checked.
//
// Components that do not exist yet are fine: they are about to be created by
// MkdirAll, and nothing can be hiding in a directory that is not there.
func checkSkillPathUnderHome(home, target string) error {
	rel, err := filepath.Rel(home, filepath.Dir(target))
	if err != nil || strings.HasPrefix(rel, "..") {
		// Not under home — a --skill-dir elsewhere, which the user named
		// explicitly and which this check has no mandate over.
		return nil
	}

	cur := home
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing below this exists yet
		}
		if err != nil {
			return fmt.Errorf("failed to inspect %s: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", errSkillPathUnsafe, cur)
		}
	}
	return nil
}
