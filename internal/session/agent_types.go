package session

import "github.com/takaaki-s/jind-ai/internal/transcript"

// Package session owns the interface + supporting types that describe how
// jind-ai talks to an interactive agent (Claude Code, Codex CLI, ...). The
// concrete implementations live under internal/agent/<kind>/; the session
// domain only knows this narrow surface so it can spawn / observe an agent
// without importing the adapter packages (import direction stays session ←
// agent, never the reverse).

// SpawnOptions is the input an Agent adapter receives when jind-ai needs to
// build the shell command that starts (or resumes) the agent inside a tmux
// pane.
type SpawnOptions struct {
	// JinSessionID is jind-ai's own session UUID. Adapters typically expose
	// it to the agent via the JIN_SESSION_ID env var so hook callbacks can
	// correlate back to a jind-ai session.
	JinSessionID string
	// AgentSessionID is the adapter-side persistent identifier (Claude
	// Code's --session-id / --resume UUID, for example). Empty on the very
	// first spawn of a fresh session.
	AgentSessionID string
	// AgentSessionStarted is true when the agent has been launched at
	// least once with AgentSessionID; adapters use it to decide between a
	// "new session" and a "resume" command line.
	AgentSessionStarted bool
	// WorkDir is the absolute directory the agent should start in (~ is
	// already expanded).
	WorkDir string
	// CustomEnv carries user-configured env vars from config.yaml. The
	// Manager forwards them to the shell command; adapters may also read
	// them if they need to.
	CustomEnv map[string]string
}

// SpawnPlan is what an Agent adapter returns to describe how to launch the
// agent. Manager splices the pieces into the fixed shell template it uses to
// wrap every session (`cd DIR; env -u ... KEY=VAL SHELL -ic 'COMMAND'`).
//
// Shell safety contract. The two fields are NOT alike, and the difference is
// the one thing to take from this comment:
//
//   - **Command is executed as a shell command. Never build it out of a value
//     you did not choose.** It ends up as the argument to `SHELL -ic`, which
//     means a shell is handed it to interpret — so `$(...)`, backticks, `;`
//     and the rest are live. The single quotes Manager wraps it in protect the
//     OUTER shell, not the command's own contents, and escaping the quotes
//     does not change that: an injection needs no quote of its own. This was
//     not theoretical. The opencode adapter concatenated a session id — a
//     value written from a hook payload without validation — and
//     `ses_x$(touch F)` ran, at whatever later moment that session resumed.
//   - **ExtraEnv values are data.** Manager single-quotes each one, so
//     arbitrary content survives verbatim: whitespace, metacharacters, quotes.
//     Pass them raw; pre-escaping is the adapter's bug, not Manager's.
//   - ExtraEnv keys and UnsetEnv entries must be POSIX env-var names matching
//     [A-Za-z_][A-Za-z0-9_]*. Manager rejects any that don't, before the
//     process is spawned.
//
// So an adapter that needs an untrusted value on the command line puts the
// value in ExtraEnv and names it from Command:
//
//	Command:  `opencode --session "$JIN_OPENCODE_SESSION"`,
//	ExtraEnv: map[string]string{"JIN_OPENCODE_SESSION": id},
//
// A shell does not re-scan the result of a parameter expansion for
// substitutions, so the value arrives as one argument however it is spelled.
// internal/session's TestBuildAgentShellCmd_ExtraEnvIsNotInterpreted checks
// that by running the command rather than by reading it.
//
// "Manager is the last line of defence" holds for ExtraEnv values and for
// key validation. It does not hold for what is inside Command, and reading it
// that way is what the paragraph above is here to prevent. Emit Command as the
// literal line you would type — including the quoting you would type around a
// value you did not choose.
type SpawnPlan struct {
	// Command is the single-line shell command that starts the agent
	// (e.g. `claude --settings /path/to/hooks.json --session-id UUID`).
	Command string
	// ExtraEnv is agent-specific KEY=VALUE pairs that must be exported for
	// the process. Manager adds them alongside the fixed JIN_SESSION_ID
	// group. Nil / empty is fine.
	ExtraEnv map[string]string
	// UnsetEnv lists env-var names that must be cleared before exec (env
	// -u NAME). Manager already unsets TMUX / TMUX_PANE unconditionally;
	// adapters add their own (Claude Code needs CLAUDECODE unset so
	// nested invocations work).
	UnsetEnv []string
}

// StatusSignal is the raw event an agent adapter interprets to decide the
// session's next Status. Manager builds it from whatever channel it caught
// the signal on (hook callback, pane output tail, ...) and hands it to
// StatusSource.Interpret.
type StatusSignal struct {
	// Kind identifies the transport: "hook" (live agent hook callback) or
	// "recover" (daemon-restart recovery asking the adapter to re-derive a
	// possibly stale status from its own persistent data). Adapters switch
	// on this and ignore signals they don't understand.
	//
	// Contract for "recover" verdicts: Manager applies only
	// StatusUpdate.Status — stale-state correction must not fire
	// notifications or touch the error field, so Notify / ErrorMessage /
	// ClearError are ignored.
	Kind string
	// Payload is an untyped key/value bag; the exact keys depend on Kind
	// and are adapter-defined. For "hook" the Manager fills in "event",
	// "notification_type", "stop_reason", "cwd"; for "recover" it fills in
	// "persisted_status", "agent_session_id", "workdir".
	Payload map[string]string
}

// StatusUpdate is the adapter's verdict on a signal: which Status the
// session should move to and whether a desktop notification should fire.
//
// ErrorMessage / ClearError work as a tri-state so adapters can distinguish
// three intents on the shared ErrorMessage field:
//
//   - ErrorMessage != ""            → set the field (adapter has a message)
//   - ClearError == true            → clear the field (agent recovered)
//   - both zero                     → leave whatever was there in place
//
// The Claude Code adapter uses the first form for StopFailure, the second
// for Stop / UserPromptSubmit / PreToolUse / PostToolUse (the pre-refactor
// invariant "any post-error progression clears the message"), and the third
// for SessionEnd / Notification (which historically never touched the
// field). Adapters that don't care about error semantics can leave both
// zero — the field remains untouched.
type StatusUpdate struct {
	Status       Status
	ErrorMessage string
	ClearError   bool
	Notify       NotifyKind
}

// NotifyKind is the abstract notification category an adapter attaches to
// a StatusUpdate. Manager forwards it unchanged as plugin.Event.NotifyKind
// (surfaced to plugin runtimes as JIN_NOTIFY_KIND / notify_kind JSON).
type NotifyKind string

const (
	// NotifyNone is the zero value; the transition carries no notification.
	NotifyNone NotifyKind = ""
	// NotifyTaskComplete signals that the assistant finished a turn.
	NotifyTaskComplete NotifyKind = "task-complete"
	// NotifyError signals that the assistant reported a failure.
	// ErrorMessage on StatusUpdate is passed through.
	NotifyError NotifyKind = "error"
	// NotifyPermission signals that the agent is blocked waiting for
	// user approval.
	NotifyPermission NotifyKind = "permission"
)

// TranscriptSource returns an agent's own conversation as jind-ai's shared
// transcript.Entry form, which is what `jin session result` serialises. How an
// adapter gets it is its own business — Claude Code writes one JSONL per
// session under ~/.claude/projects, Codex writes a date-sharded rollout, and
// opencode keeps its conversation in a database and is asked to print it — so
// the translation into Entry/Block lives with the adapter and only the result
// shape is common.
//
// Contract, matching what transcript.Reader already does:
//
//   - since is an exclusive lower bound compared as a string. An entry whose
//     Timestamp is <= since is dropped, so passing the last timestamp already
//     seen yields only what came after it.
//   - A session that has no log file yet returns (nil, nil), not an error.
//     "The agent has not written anything" is a state every session passes
//     through, and it is not a failure.
//   - Errors are for genuine read failures only.
//
// workDir is a hint, not a key: an implementation is free to ignore it if it
// locates the log by session ID alone.
//
// What belongs in an Entry: the conversation as an operator would read it.
// Context the agent injected on the operator's behalf is not conversation —
// environment blocks, skill bodies, system prompts — and neither are a
// subagent's own turns, nor the agent's internal bookkeeping.
//
// A reader has two lawful ways to honour that, and which one it picks is its
// own business:
//
//   - Drop them while reading. The Codex reader does this, so its entries are
//     conversation by construction and it leaves Entry.Injected /
//     Entry.Sidechain false.
//   - Emit them with Entry.Injected / Entry.Sidechain set. The Claude Code
//     reader does this, because it also feeds `jin session result`, which has
//     always returned every line — narrowing it would change what every
//     existing Claude Code session reports. Shared views over []Entry skip
//     flagged entries, so the operator-facing answer is the same either way.
//
// **A reader that cannot classify an entry must drop it, never emit it
// unflagged.** Injected == false is read everywhere as "checked, and this is
// the operator's", not as "unknown" — emitting an unclassified injection puts
// it straight into the caller's view of what the operator said. That failure
// was measured: deriving the previews without provenance surfaced the body of
// an invoked skill as the last user message on 55 of 231 real transcripts.
// The flags are a conclusion a reader reached, so declining to reach one means
// leaving the entry out, not defaulting it.
//
// One method, deliberately. Views over a conversation — last message, last N
// exchanges, truncation — are kind-independent policy and belong in shared
// functions over []Entry, not here. Adding them would make every adapter
// re-implement exchange boundaries, which is how the same flag ends up meaning
// different things per agent kind.
//
// What the one method does NOT say is what a read costs, and the callers differ by
// orders of magnitude. `jin session result` is one command an orchestrator
// chose to run, so a read that takes a second is fine. A preview decorates
// every row of `session list`, which the TUI refreshes on a timer, so the
// budget there is per-session-per-refresh. An implementation that shells out
// satisfies the first and ruins the second. PollableTranscriptSource is how a
// reader says which it is.
type TranscriptSource interface {
	ReadEntries(workDir, sessionID, since string) ([]transcript.Entry, error)
}

// PollableTranscriptSource is a TranscriptSource whose ReadEntries is cheap
// enough to call on a timer, once per session per refresh.
//
// Reading a local file qualifies; spawning a process does not. The opencode
// adapter asks opencode to print the session, so it deliberately does not
// implement this: on a list of opencode sessions refreshed every two seconds, a
// preview would mean one process per row per refresh, permanently. The measured
// cost of that read is quoted once, where it is made — see exportTimeout in
// internal/agent/opencode.
//
// Opt-in rather than opt-out, and that direction is the whole design. An
// adapter that forgets to declare itself loses its previews — visible, and
// harmless. The opposite default would let a new expensive reader melt the
// list, and no test on either side would catch it, because neither the reader
// nor the preview is wrong on its own.
//
// Callers on a polling path must type-assert for this interface and skip the
// source when it is absent. Callers with a per-command budget — handleResult —
// must not: refusing to read there would turn a slow answer into no answer.
type PollableTranscriptSource interface {
	TranscriptSource
	// CheapEnoughToPoll declares the fact by existing, and returns nothing on
	// purpose. A bool would let a reader answer false, which says exactly what
	// not implementing the interface already says — one fact with two spellings,
	// and a branch at every caller for the one that never happens.
	CheapEnoughToPoll()
}

// StatusSource translates raw StatusSignals into StatusUpdates. Adapters
// return (StatusUpdate{}, false) when a signal is meaningful but does not
// warrant a Status change (Manager still applies side effects such as CWD
// tracking).
type StatusSource interface {
	Interpret(StatusSignal) (StatusUpdate, bool)
}

// SetupContext is the input to Agent.Setup, called once per session start
// before the shell command is built. Adapters use it to write agent-side
// config files (Claude Code's hooks-settings.json, trust dialog state, ...).
type SetupContext struct {
	StateDir string // jind-ai's persistent state directory (~/.local/state/jind-ai)
	ExecPath string // absolute path to the running jin binary (os.Executable())
	WorkDir  string // absolute working directory the session will start in
}

// BlockKind identifies the sort of blocking prompt an agent's TUI is showing
// while it waits for a person to answer.
//
// Not every kind can be answered. A kind exists for a screen jin can only
// RECOGNISE, precisely so RespondToBlock can refuse it by name instead of
// typing into it. Recognising more than we can drive is the point of the
// enum, not an oversight in it.
//
// A screen the adapter cannot classify must come back as BlockNone, because
// BlockNone is what makes RespondToBlock send nothing at all. Every way of
// being unsure therefore costs a refusal rather than keys landing somewhere
// unknown.
type BlockKind string

const (
	// BlockNone means the capture shows no blocking prompt.
	BlockNone BlockKind = ""
	// BlockPermission is a tool-approval dialog offering numbered choices.
	BlockPermission BlockKind = "tool-permission"
	// BlockQuestion is one question with numbered answers.
	BlockQuestion BlockKind = "question"
	// BlockQuestionMulti is several questions gathered into one form.
	// Recognised but not answerable: answering one question leaves the form
	// standing, so "did the block clear?" — the only post-condition
	// RespondToBlock has — cannot tell a half-answered form from an answer
	// that never landed.
	BlockQuestionMulti BlockKind = "question-multi"
	// BlockQuestionSubmit is such a form waiting on its final confirmation.
	// Recognised but not answerable, for the same reason.
	BlockQuestionSubmit BlockKind = "question-submit"
)

// Answerable reports whether RespondToBlock can drive this kind.
//
// It is deliberately a whitelist: a kind added later is unanswerable until
// someone writes it in here, which is the safe direction for a value that
// decides whether keys reach an approval dialog.
func (k BlockKind) Answerable() bool {
	return k == BlockPermission || k == BlockQuestion
}

// BlockAnswer is the answer a caller wants to give a blocking prompt.
// Exactly one field carries it; the daemon rejects a request that sets both
// or neither, so an adapter may assume it received one of the two.
type BlockAnswer struct {
	// Option is a choice's on-screen number, 1-based.
	Option int
	// Text is free text, for a prompt that offers a free-text entry.
	Text string
}

// KeyStep is one step of the key sequence that answers a blocking prompt.
// Exactly one of Key and Literal is set.
type KeyStep struct {
	// Key is a tmux key name ("Enter", "Down"), sent via SendKeys.
	Key string
	// Literal is text typed verbatim, sent via SendKeysLiteral.
	Literal string
	// Verify asks Manager to confirm this step's Literal rendered into the
	// pane before it runs the next step, and to abandon the sequence when it
	// did not.
	//
	// Set it only where the adapter has measured that the agent draws the
	// text. A step whose effect cannot be seen must not claim it can: the
	// steps after it — a committing Enter, typically — would then run on a
	// guess about what the earlier ones did.
	Verify bool
}

// Agent is the interface every agent adapter satisfies. The Manager holds it
// via AgentResolver and never imports a concrete adapter package.
//
// Implementations must be safe for concurrent use: Setup and SpawnCommand
// may be invoked from multiple goroutines (per-session goroutines that
// captureOutputTmux spawns).
type Agent interface {
	// Kind returns the short identifier stored in Session.AgentKind
	// ("claude", "codex", ...).
	Kind() string
	// Setup prepares any agent-global or per-workDir state that must exist
	// before the process is spawned. Called once per startSessionTmux
	// invocation. Errors are logged but do not abort the launch — see the
	// Claude Code adapter for the intended failure semantics.
	Setup(SetupContext) error
	// SpawnCommand returns the shell command + env additions that launch
	// (or resume) the agent for the given session.
	SpawnCommand(SpawnOptions) SpawnPlan
	// RecognizesSessionID reports whether id is written the way this
	// adapter's agent writes its own session ids. Manager asks before
	// letting a hook payload re-key Session.AgentSessionID, so an id that
	// belongs to no agent — or to a different one — never lands in the
	// record, in a resume command line, or in a transcript lookup.
	//
	// Shape, not ownership. This cannot tell one live session's id from
	// another's — a well-formed id belonging to a different session of the
	// same kind passes, and Manager has no way to know. What the gate
	// narrows is which VALUES can be recorded, not which session an event
	// may speak for.
	//
	// Answer the LOOSE question, not the exact one. Manager applies a
	// kind-independent safety gate first (see safeAgentSessionID), which
	// rules out shell metacharacters, path traversal and leading-hyphen
	// flag lookalikes — so this predicate is free to accept anything shaped
	// like an id this agent could mint, including formats it has not
	// shipped yet. Being wrong in the strict direction is the expensive
	// one: a refused id is never recorded, so the session keeps whatever it
	// held before, and what that costs differs per adapter:
	//
	//   - Claude Code is told its id (--session-id), so the value already
	//     held IS the right one and a refusal costs nothing.
	//   - Codex mints its own, so a refusal leaves the pre-minted UUID,
	//     `codex resume` fails within seconds, and the quick-fail retry
	//     starts a fresh session — a visible restart, not a silent one.
	//   - opencode mints its own AND resumes silently, so a refusal starts a
	//     new session with the operator's conversation simply absent. That
	//     is why its answer here is deliberately the same loose prefix test
	//     its resume path already gates on, and not the stricter alphabet
	//     check beside it: every id the write gate accepts is an id the
	//     resume path would use, so no new silent failure is introduced.
	//
	// On Agent rather than a side interface for the same reason as
	// ClearInputKeys: an adapter that forgets this should fail to compile,
	// because what it reintroduces is an unvalidated write.
	RecognizesSessionID(id string) bool
	// StatusSource returns the adapter's interpreter for StatusSignals.
	// Must never return nil (agents that don't observe status can return a
	// no-op implementation).
	StatusSource() StatusSource
	// Description returns the adapter's Layer C description enhancer, or
	// nil if the adapter cannot produce structured descriptions.
	Description() DescriptionEnhancer
	// Transcript returns the adapter's reader for the agent's own on-disk
	// conversation log, or nil when this adapter cannot read one.
	//
	// nil means "cannot read", never "the conversation is empty". The two
	// are not the same thing and the difference is the whole point: `jin
	// session result` used to call the Claude Code reader unconditionally,
	// so every non-Claude session answered with zero entries and success —
	// indistinguishable from a child agent that ran and said nothing. An
	// orchestrator reading that concludes the work produced no output,
	// which is a wrong answer delivered quietly. An error is a wrong answer
	// delivered loudly, and loudly is recoverable.
	//
	// A caller answering *with* the conversation must therefore fail on nil
	// — that is `session result`. A caller merely decorating something it
	// has to render anyway may stay silent, which is what
	// Manager.AttachLastMessages does for the list rows and `session info`:
	// failing a whole `session list` because one session's log is
	// unreadable would be worse than a row with a blank second line. The
	// obligation that survives in both cases is never to dress nil up as an
	// empty conversation, so a silent caller must not be the only way an
	// operator can ask.
	//
	// On Agent rather than a side interface for the same reason as
	// ClearInputKeys: an adapter that forgets this should fail to compile,
	// because what it reintroduces is silence.
	Transcript() TranscriptSource
	// ClearInputKeys returns the tmux key names (SendKeys form — e.g.
	// "C-u", "C-a", "BSpace" — not literal text) that clear this adapter's
	// TUI input line to empty. Manager.SendPrompt sends these before each
	// send attempt so residual text in the input area cannot concatenate
	// with the new prompt.
	//
	// Return nil (or an empty slice) to opt out: SendPrompt then falls
	// through to its pre-refactor behaviour and the residual-concat risk
	// documented in docs/gotchas.md "Session send" applies. Adapters whose
	// TUI has no safe clear sequence — for example one that rebinds C-u —
	// should return nil rather than sending keys with side effects.
	//
	// This method is on Agent (not a separate PromptClearer interface) so
	// the compiler catches adapters that forget to implement it: silent
	// drift would let residual concat regress unnoticed when a new adapter
	// lands. Opt-out is explicit: return nil.
	ClearInputKeys() []string
	// PastePlaceholder returns the text this adapter's TUI will show for
	// prompt when it arrives as a single bracketed paste, or "" to receive
	// prompts as literal keystrokes instead (the default).
	//
	// Returning a placeholder selects the paste transport. A TUI usually
	// collapses a large paste into a summary line, so the prompt text is not
	// on screen at all and SendPrompt's usual tail match cannot succeed;
	// what it looks for instead is exactly the string returned here. That
	// makes this a statement of fact about the agent — "paste this and you
	// will see that" — with the adapter owning both the wording and whatever
	// quantity it embeds, and the manager owning only the comparison.
	//
	// Worth it only where typing is pathologically expensive — OpenCode is
	// the one shipped adapter where it is, by two orders of magnitude (see
	// docs/gotchas.md "Session send"). Claude Code could not use this
	// anyway: its summary numbers pastes ("#1", "#2") rather than measuring
	// them, so it is not predictable from the prompt.
	//
	// The returned text is matched after SendPrompt's usual normalization,
	// so it need not account for line wrapping or the box-drawing a TUI
	// paints around its input.
	PastePlaceholder(prompt string) string
	// DismissOverlayKeys returns the tmux key names that close any completion
	// overlay this adapter's TUI leaves open once prompt has been typed in
	// full, or nil when prompt cannot open one.
	//
	// This exists because SendPrompt's verify proves the wrong thing. It
	// proves the prompt's tail is rendered in the input area — which is NOT
	// the same as "Enter will submit it". Measured on Claude Code 2.1.224, a
	// prompt ending in an in-progress completion token leaves an overlay
	// open, and Enter is then consumed to accept a candidate: the prompt is
	// rewritten in place, never submitted, and SendPrompt still returns nil
	// (3/3). SendPrompt sends these keys after verify succeeds and before
	// Enter, then re-checks that the prompt survived.
	//
	// The prompt is a parameter because the answer depends on it, and
	// because the key is not free: on Claude Code, Escape also interrupts a
	// running turn (2/3 — the third run's turn ended first). An adapter
	// should return keys only for prompts that can actually open an overlay,
	// so the side effect never reaches prompts that had no overlay to close.
	//
	// Return nil (or an empty slice) to opt out. That is the correct answer
	// for an adapter whose overlay behaviour has not been measured: sending a
	// key on a guess is how this class of bug gets introduced, not fixed.
	//
	// On Agent rather than a side interface for the same reason as
	// ClearInputKeys — an adapter that forgets this should fail to compile,
	// because the failure it reintroduces is silent.
	DismissOverlayKeys(prompt string) []string
	// DetectBlock reports which blocking prompt the captured pane shows, or
	// BlockNone when it shows none.
	//
	// Manager asks this both questions it has to settle — "is there anything
	// to answer?" before it sends a key, and "did the answer take?" after —
	// so the two can never be judged by different rules. SendPrompt folds its
	// own pair of "did the prompt land?" checks into one closure for the same
	// reason.
	//
	// What makes that safe is where the uncertainty lands. Manager sends
	// nothing on BlockNone, so a screen the adapter does not recognise costs
	// a refusal; a screen it recognises as unanswerable costs a refusal that
	// can say why. Only a positive, answerable verdict puts keys in the pane,
	// which is why an adapter should order its checks so the kinds it cannot
	// drive are ruled out first.
	//
	// The parameter is the capture rather than a session, so this stays a
	// pure function of what was on screen: adapter tests need a string, not
	// a tmux server.
	//
	// Return BlockNone unconditionally to opt out.
	DetectBlock(capture string) BlockKind
	// AnswerBlockKeys returns the keys that answer kind with ans, or an error
	// explaining why this agent cannot express that answer.
	//
	// capture is the same snapshot DetectBlock classified, not a fresh one,
	// so an adapter that has to read the screen — to learn which number a
	// free-text entry carries, say — reads the frame the verdict was made
	// against rather than a later one that may have moved.
	//
	// An error is a refusal, and Manager has sent nothing by the time it
	// arrives. The message is the entirety of what the caller gets, so it
	// should name what to do instead rather than only what failed.
	//
	// On Agent rather than a side interface for the same reason as
	// ClearInputKeys and Transcript: an adapter that forgets it should fail
	// to compile. Silently answering nothing is exactly the failure this
	// exists to remove.
	//
	// Return an error unconditionally to opt out.
	AnswerBlockKeys(kind BlockKind, capture string, ans BlockAnswer) ([]KeyStep, error)
}

// AgentResolver bridges the Manager to the process-global agent registry
// that lives in internal/agent. The daemon injects a thin implementation
// that delegates to agent.Lookup; the session package never sees the
// registry itself, keeping the import direction one-way.
type AgentResolver interface {
	Resolve(kind string) (Agent, error)
}
