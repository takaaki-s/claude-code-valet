/**
 * jind-ai status reporter for opencode.
 *
 * This file is embedded in the `jin` binary and materialised at
 * <state>/opencode/plugin/jin.ts on every session start. Do not edit the
 * installed copy — edit internal/agent/opencode/plugin/jin.ts and rebuild.
 *
 * EVERY EXPORT IN THIS FILE MUST BE A FUNCTION. Because the module has no
 * default export, opencode falls back to getLegacyPlugins(), which walks
 * Object.values(mod) and throws `Plugin export is not a function` on the
 * first export that is neither a function nor an object exposing .server
 * (packages/opencode/src/plugin/index.ts). One `export const VERSION = "1"`
 * would take the whole plugin down, and opencode swallows a load failure as
 * a warning — so the symptom is status silently never updating. The name
 * `server` is conventional here, not required: only the default-export path
 * (used by npm-packaged plugins) checks names.
 *
 * opencode's bus vocabulary does not match the canonical event names
 * jind-ai's Manager keys its side effects on, so this plugin normalises
 * before calling `jin hook`:
 *
 *   session.created            -> SessionStart       (carries the real ses_ id)
 *   session.status {busy,...}  -> UserPromptSubmit   (thinking)
 *   session.status {idle}      -> (dropped)          see note 1
 *   session.idle               -> Stop               (idle)
 *   permission.asked           -> PermissionRequest  (permission)
 *   permission.replied         -> UserPromptSubmit   (back to thinking)
 *   session.error              -> StopFailure        (idle + error)
 *
 * Four behaviours of opencode drive the shape of this file:
 *
 *  1. Going idle publishes BOTH session.status{type:"idle"} and
 *     session.idle. Only session.idle is mapped, so a turn reports exactly
 *     one Stop.
 *  2. session.status{type:"busy"} fires once per step — ~9 times for a
 *     single trivial turn. Sending each one would multiply daemon IPC for
 *     no gain, so consecutive duplicates of the same canonical event are
 *     suppressed per session.
 *  3. The task tool creates CHILD sessions (tool/task.ts passes parentID)
 *     that publish session.created / session.status / session.idle on the
 *     same bus. Forwarding those is actively harmful: Manager re-keys
 *     Session.AgentSessionID on any hook whose session_id differs, so a
 *     child's SessionStart repoints the jin session at the subagent and
 *     breaks resume, and a child's idle reports the turn finished while
 *     the parent is still working. jind-ai therefore reports on an
 *     ALLOW-LIST of root sessions, never on "everything except known
 *     children" — see docs/gotchas.md for why the deny-list version is
 *     unsound.
 *  4. session.error's sessionID is optional in the schema, so errors are
 *     attributed to the last root session seen rather than discarded.
 *
 * The plugin is a pure observer. It deliberately uses the `event` hook
 * rather than the `permission.ask` hook: the latter can rewrite the
 * user's allow/deny decision, and a status reporter has no business on
 * that path.
 *
 * Deliberately NOT sent: `cwd`. The Claude and Codex adapters include it
 * so Manager can track worktree moves, but jind-ai already polls tmux's
 * pane_current_path for the same purpose, and opencode gives the plugin
 * only its start-up directory — which would go stale rather than track.
 */

import { spawn } from "node:child_process"

/** Absolute path to the jin binary; substituted, already quoted, at materialisation time. */
const JIN_BIN = __JIN_BIN__

/**
 * Marks an opencode that the agent started for itself, so this plugin can
 * decline to report from inside it. Set on the way out, in shell.env, and read
 * on the way in, at the top of server().
 *
 * jind-ai's own spawns clear it, and must: see the adapter's nestedEnv. Why a
 * mark at all, rather than withholding the config dir the nested process loads
 * this file from, is in docs/gotchas.md.
 */
const NESTED_ENV = "JIN_OPENCODE_NESTED"

/**
 * How long to wait for `jin hook` before killing it. The command connects
 * to the daemon socket and returns, so this is a backstop against a daemon
 * that accepts but never answers: without it one stuck invocation would
 * block the send queue and, with it, this plugin's event handler.
 */
const HOOK_TIMEOUT_MS = 3000

/**
 * Budget for asking opencode to classify a session id we have not seen
 * created. Bounded because the lookup happens inline in the event handler:
 * a wedged server must degrade to "no status updates", never to "opencode
 * stops dispatching events".
 */
const LOOKUP_TIMEOUT_MS = 2000

type CanonicalEvent =
  | "SessionStart"
  | "UserPromptSubmit"
  | "PermissionRequest"
  | "Stop"
  | "StopFailure"

/**
 * Maps an opencode bus event to a canonical jind-ai event, or undefined
 * when jind-ai does not care about it (the majority: message.part.delta,
 * plugin.added, catalog.updated, ...).
 *
 * session.created is handled by the caller instead: deciding whether it is
 * a root or a child session needs state this function does not have.
 */
function canonical(type: string, properties: any): CanonicalEvent | undefined {
  switch (type) {
    case "session.status":
      // Idle is reported through session.idle instead, so this arm only
      // recognises "the agent is working".
      return properties?.status?.type === "idle" ? undefined : "UserPromptSubmit"
    case "session.idle":
      return "Stop"
    case "permission.asked":
      return "PermissionRequest"
    case "permission.replied":
      return "UserPromptSubmit"
    case "session.error":
      return "StopFailure"
    default:
      return undefined
  }
}

/**
 * Invokes `jin hook` with the payload on stdin. Never rejects; resolves
 * true only when the command exited cleanly, so the caller can decide
 * whether the suppression cache may remember this event.
 *
 * Uses child_process rather than Bun's `$` so the binary path is passed as
 * argv rather than through a shell, which keeps paths containing spaces or
 * quotes safe without any escaping here.
 */
function sendHook(sessionID: string, event: CanonicalEvent): Promise<boolean> {
  return new Promise((resolve) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const done = (ok: boolean) => {
      if (settled) return
      settled = true
      if (timer) clearTimeout(timer)
      resolve(ok)
    }

    try {
      // No env override: JIN_SESSION_ID is already in process.env — it is
      // where jinSessionID was read from — and the child inherits it.
      const child = spawn(JIN_BIN, ["hook"], { stdio: ["pipe", "ignore", "ignore"] })
      timer = setTimeout(() => {
        try {
          child.kill("SIGKILL")
        } catch {}
        done(false)
      }, HOOK_TIMEOUT_MS)
      child.on("error", () => done(false))
      child.on("close", (code) => done(code === 0))
      child.stdin.on("error", () => done(false))
      child.stdin.end(
        JSON.stringify({
          session_id: sessionID,
          hook_event_name: event,
        }),
      )
    } catch {
      done(false)
    }
  })
}

export const server = async (input?: { client?: any }) => {
  const client = input?.client
  // Started by the agent rather than by jind-ai. It has JIN_SESSION_ID and the
  // config dir by inheritance, and a root session of its own that the parentID
  // allow-list cannot recognise as foreign — so without this it would report
  // the parent's session as started, thinking, and idle on its own schedule.
  if (process.env[NESTED_ENV]) return {}
  const jinSessionID = process.env["JIN_SESSION_ID"]
  // Not a jind-ai managed session: contribute nothing at all. Users who run
  // opencode themselves must not have their process touched by this plugin,
  // even though `jin hook` would also no-op without the variable.
  if (!jinSessionID) return {}

  /** Last canonical event reported per session, for duplicate suppression. */
  const lastSent = new Map<string, CanonicalEvent>()

  /**
   * Root session id most recently classified, used to attribute a
   * session.error whose own sessionID is absent (it is optional in
   * opencode's schema). Never substitute an arbitrary id here — Manager
   * re-keys Session.AgentSessionID to whatever it receives.
   */
  let lastRootSessionID = process.env["JIN_OPENCODE_ROOT_SESSION"] || undefined

  /**
   * Classification cache: true = report on this session, false = subagent,
   * absent = not yet classified. See docs/gotchas.md ("opencode adapter")
   * for why an allow-list is the only sound shape here.
   */
  const sessionIsRoot = new Map<string, boolean>()

  /** In-flight classifications, so overlapping handlers share one lookup. */
  const pendingLookups = new Map<string, Promise<boolean>>()

  if (lastRootSessionID) sessionIsRoot.set(lastRootSessionID, true)

  /** The single place classification is recorded. */
  const classify = (id: string, isRoot: boolean): boolean => {
    sessionIsRoot.set(id, isRoot)
    if (isRoot) lastRootSessionID = id
    return isRoot
  }

  /** Asks opencode whether `id` has a parent. Answers false on any failure. */
  const lookup = async (id: string): Promise<boolean> => {
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      const info: any = await Promise.race([
        client.session.get({ path: { id } }),
        new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error("timeout")), LOOKUP_TIMEOUT_MS)
        }),
      ])
      if (!info?.data) return false
      return classify(id, !info.data.parentID)
    } catch {
      // Deliberately not cached: a wedged or still-starting server must not
      // permanently mark a real root as unreportable.
      return false
    } finally {
      if (timer) clearTimeout(timer)
    }
  }

  /**
   * Decides whether events for `id` should be reported, asking opencode
   * when the plugin has no record of the session.
   */
  const isRootSession = (id: string): boolean | Promise<boolean> => {
    const known = sessionIsRoot.get(id)
    if (known !== undefined) return known
    if (!client?.session?.get) return false

    let pending = pendingLookups.get(id)
    if (!pending) {
      pending = lookup(id).finally(() => pendingLookups.delete(id))
      pendingLookups.set(id, pending)
    }
    return pending
  }

  /**
   * Per-session send queues. Status is order-sensitive — a Stop that
   * overtakes the UserPromptSubmit before it would leave the session stuck
   * showing "thinking" — but only within a session, so one global queue
   * would let a slow `jin hook` stall unrelated sessions for
   * HOOK_TIMEOUT_MS.
   */
  const queues = new Map<string, Promise<void>>()

  const enqueue = (sessionID: string, event: CanonicalEvent) => {
    // Recorded before the send so a burst of identical events collapses
    // even while the first is still in flight; cleared again on failure so
    // a dropped report (daemon restarting) is retried on the next event
    // rather than suppressed forever.
    lastSent.set(sessionID, event)
    const queued = (queues.get(sessionID) ?? Promise.resolve()).then(async () => {
      const ok = await sendHook(sessionID, event)
      if (!ok && lastSent.get(sessionID) === event) lastSent.delete(sessionID)
    })
    queues.set(sessionID, queued)
    return queued
  }

  return {
    "shell.env": async (_input: unknown, output: { env?: Record<string, string> }) => {
      // opencode always passes an env object today; the guard keeps a
      // future shape change from throwing on the agent's shell path.
      if (!output?.env) return
      // The pane already exports JIN_SESSION_ID, so this is belt-and-braces
      // against opencode narrowing the env it hands to child processes.
      output.env["JIN_SESSION_ID"] = jinSessionID
      // Anything the agent starts is, by definition, not the session pane's
      // own opencode — so mark it. See NESTED_ENV and docs/gotchas.md.
      output.env[NESTED_ENV] = "1"
    },

    event: async ({ event }: { event: { type: string; properties?: any } }) => {
      const props = event?.properties
      if (!props) return

      if (event.type === "session.deleted") {
        const gone = props.sessionID
        if (gone) {
          lastSent.delete(gone)
          sessionIsRoot.delete(gone)
          queues.delete(gone)
          if (lastRootSessionID === gone) lastRootSessionID = undefined
        }
        return
      }

      if (event.type === "session.created") {
        const created = props.sessionID
        if (!created) return
        // A parentID means the task tool spawned this as a subagent; it is
        // classified false and stays invisible to jind-ai from here on.
        if (!classify(created, !props.info?.parentID)) return
        await enqueue(created, "SessionStart")
        return
      }

      const next = canonical(event.type, props)
      if (!next) return

      // session.error may omit its sessionID; fall back to the newest root.
      const sessionID = props.sessionID ?? (next === "StopFailure" ? lastRootSessionID : undefined)
      if (!sessionID) return
      // Suppression first: a repeat of the last reported event needs no
      // classification, which keeps the ~9 busy events of one turn from
      // reaching the lookup at all.
      if (lastSent.get(sessionID) === next) return
      if (!(await isRootSession(sessionID))) return
      if (lastSent.get(sessionID) === next) return

      await enqueue(sessionID, next)
    },
  }
}
