package plugin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/takaaki-s/jind-ai/internal/debug"
	"github.com/takaaki-s/jind-ai/internal/jinenv"
	"github.com/takaaki-s/jind-ai/pkg/plugin/manifest"
)

var pluginLog = debug.NewLogger("plugin-debug.log")

// maxDepth bounds direct plugin→plugin chains: a plugin runs at depth 1, so a
// run it requests would land at depth 2 and is rejected. Depth cannot follow
// the indirect loop (plugin → `jin session send` → agent → hook) because the
// environment does not survive the agent process; the debounce window is the
// primary guard for that path.
//
// It does not follow a pane either. A pane opened by `jin pane popup` / `jin
// pane split` is given the four identity variables and deliberately not this
// one, so a plugin that runs another plugin from inside a popup starts it back
// at depth 1. That is not a regression — the pane used to be given nothing at
// all — but it is now a choice rather than an oversight: JIN_PLUGIN_DEPTH is
// not part of "which jin am I calling back into", and forwarding it on the
// `--here` path alone would guard one of the two ways in. Debounce remains the
// guard here, as it is for the indirect loop.
const maxDepth = 2

// DefaultDebounce is the minimum interval between deliveries of the same
// (plugin, action, session, event) tuple when the caller does not configure one.
const DefaultDebounce = 3 * time.Second

// debouncePruneThreshold caps lastFired growth: sessions come and go for the
// daemon's whole lifetime, so once the map crosses this size expired entries
// are swept on the next debounce check. Entries past their window carry no
// information, making the sweep free of behaviour change.
const debouncePruneThreshold = 128

// PopupSizeResolver resolves the popup size a plugin action should receive as
// JIN_PLUGIN_POPUP_* env when it runs. Returning empty strings means "no
// explicit size" and the caller of `jin pane popup --here` falls through to
// tmux's built-in default. The resolver takes precedence in the order:
// user config > manifest declaration > global plugin default > hardcoded.
// actionID identifies which of the plugin's actions is running so resolvers
// can look up per-action user config.
type PopupSizeResolver func(pluginName, actionID string, m *manifest.PopupConfig) (width, height string)

// EventDispatcher fans events out to installed plugins. Publish never blocks:
// registry reads and plugin processes run on background goroutines, and every
// failure is logged rather than returned (fail-open — a broken plugin must not
// stall the status pipeline).
type EventDispatcher struct {
	registry      *Registry
	pluginsDir    string
	stateDir      string
	identity      jinenv.Identity
	debounce      time.Duration
	popupResolver PopupSizeResolver

	mu        sync.Mutex
	lastFired map[string]time.Time
	warned    map[string]bool
}

// NewDispatcher returns a dispatcher that resolves plugins through registry
// and injects identity into every run. debounce <= 0 selects DefaultDebounce.
// A nil popupResolver is replaced with one that always returns empty strings
// (no popup size hints exported).
//
// identity is supplied by the caller so that a plugin and an agent started by
// the same daemon get the same one.
func NewDispatcher(registry *Registry, pluginsDir, stateDir string, identity jinenv.Identity, debounce time.Duration, popupResolver PopupSizeResolver) *EventDispatcher {
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	if popupResolver == nil {
		popupResolver = func(string, string, *manifest.PopupConfig) (string, string) { return "", "" }
	}
	return &EventDispatcher{
		registry:      registry,
		pluginsDir:    pluginsDir,
		stateDir:      stateDir,
		identity:      identity,
		debounce:      debounce,
		popupResolver: popupResolver,
		lastFired:     make(map[string]time.Time),
		warned:        make(map[string]bool),
	}
}

// Publish implements Dispatcher.
func (d *EventDispatcher) Publish(ev Event) {
	go d.publish(ev)
}

func (d *EventDispatcher) publish(ev Event) {
	entries, err := d.registry.Load()
	if err != nil {
		d.warnOnce("registry", "plugin registry load failed: %v", err)
		return
	}
	for _, e := range entries {
		switch e.State {
		case StateEnabled:
			// handled below
		case StateIncompatible, StateBroken:
			d.warnOnce(e.Name+"|"+e.State.String(), "plugin %s skipped (%s): %v", e.Name, e.State, e.Err)
			continue
		default:
			continue
		}
		for i := range e.Manifest.Actions {
			a := &e.Manifest.Actions[i]
			if !d.matches(a, ev) {
				continue
			}
			if !d.passDebounce(e.Name, a.ID, ev) {
				pluginLog("plugin %s:%s debounced for %s %s:%s", e.Name, a.ID, ev.SessionID, ev.Name, ev.Status)
				continue
			}
			go d.run(e, a, ev, 1, ActionContext{})
		}
	}
}

// RunAction executes one plugin action on demand (the `jin plugin run` path).
// It bypasses matcher and debounce but still enforces state and depth checks.
// actionID selects which of the plugin's actions runs; "" means the default
// action (actions[0]) and an unknown id is a synchronous error. The run
// itself is async. actx carries the invoking CLI's tmux context (empty when
// not applicable).
func (d *EventDispatcher) RunAction(name, actionID string, ev Event, callerDepth int, actx ActionContext) error {
	if callerDepth+1 >= maxDepth {
		return fmt.Errorf("plugin %s not run: depth limit reached (JIN_PLUGIN_DEPTH=%d) — plugins cannot chain plugin runs", name, callerDepth)
	}
	entries, err := d.registry.Load()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		switch e.State {
		case StateEnabled:
			var a *manifest.Action
			if actionID == "" {
				a = e.Manifest.DefaultAction()
				if a == nil {
					return fmt.Errorf("plugin %s has no actions", name)
				}
			} else {
				a = e.Manifest.FindAction(actionID)
				if a == nil {
					return fmt.Errorf("plugin %s has no action %q (available: [%s])",
						name, actionID, strings.Join(e.Manifest.ActionIDs(), ", "))
				}
			}
			go d.run(e, a, ev, callerDepth+1, actx)
			return nil
		case StateIncompatible:
			return fmt.Errorf("plugin %s is incompatible: %v (try: jin plugin update %s)", name, e.Err, name)
		case StateBroken:
			return fmt.Errorf("plugin %s is broken: %v", name, e.Err)
		default:
			return fmt.Errorf("plugin %s is disabled", name)
		}
	}
	return fmt.Errorf("plugin %s is not installed", name)
}

func (d *EventDispatcher) run(e Entry, a *manifest.Action, ev Event, depth int, actx ActionContext) {
	timeout := e.Manifest.EffectiveTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	popupWidth, popupHeight := d.popupResolver(e.Name, a.ID, a.Popup)

	err := ExecPlugin(ctx, ExecOptions{
		PluginDir:   filepath.Join(d.pluginsDir, e.Name),
		Run:         a.Entrypoint,
		ActionID:    a.ID,
		Env:         ev,
		Caller:      actx,
		Depth:       depth,
		Identity:    d.identity,
		LogPath:     LogPath(d.stateDir, e.Name),
		Timeout:     timeout,
		PopupWidth:  popupWidth,
		PopupHeight: popupHeight,
	})
	if err != nil {
		d.warnOnce(e.Name+"|"+a.ID+"|"+err.Error(), "plugin %s:%s failed: %v", e.Name, a.ID, err)
	}
}

func (d *EventDispatcher) matches(a *manifest.Action, ev Event) bool {
	for _, matcher := range a.On {
		if manifest.MatcherMatches(matcher, ev.Name, ev.Status) {
			return true
		}
	}
	return false
}

// passDebounce reports whether the (plugin, action, session, event) tuple is
// outside its debounce window, and records the firing time when it is.
func (d *EventDispatcher) passDebounce(name, actionID string, ev Event) bool {
	key := name + "\x00" + actionID + "\x00" + ev.SessionID + "\x00" + ev.Name + ":" + ev.Status
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.lastFired[key]; ok && now.Sub(last) < d.debounce {
		return false
	}
	if len(d.lastFired) >= debouncePruneThreshold {
		for k, ts := range d.lastFired {
			if now.Sub(ts) >= d.debounce {
				delete(d.lastFired, k)
			}
		}
	}
	d.lastFired[key] = now
	return true
}

// warnOnce logs a warning once per key for the daemon's lifetime, so a
// persistently broken plugin does not flood the log on every event.
func (d *EventDispatcher) warnOnce(key, format string, args ...any) {
	d.mu.Lock()
	seen := d.warned[key]
	d.warned[key] = true
	d.mu.Unlock()
	if !seen {
		pluginLog(format, args...)
	}
}
