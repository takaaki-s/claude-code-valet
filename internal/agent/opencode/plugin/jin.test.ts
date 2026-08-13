/**
 * Routing tests for the embedded jind-ai plugin.
 *
 * Run via `bun test` — the Go side drives this from
 * TestPluginRouting_BunTest, which skips when bun is unavailable. This file
 * is never materialised into a session: WritePlugin copies only jin.ts, and
 * opencode's glob only ever sees that one file.
 *
 * The plugin's whole job is deciding which opencode bus events become which
 * `jin hook` calls, and getting that wrong is silent — a subagent's events
 * leaking through repoints jind-ai's session id and breaks resume with no
 * error anywhere. So these tests assert on what actually reached the hook
 * binary, using a stub in place of `jin`.
 */

import { afterAll, beforeAll, expect, test } from "bun:test"
import { mkdtempSync, readFileSync, rmSync, writeFileSync, existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const workDir = mkdtempSync(join(tmpdir(), "jin-plugin-test-"))
const stubPath = join(workDir, "stub-jin")
const logPath = join(workDir, "hook.log")
let modulePath: string

beforeAll(() => {
  writeFileSync(
    stubPath,
    `#!/bin/sh\ncat >> "$JIN_HOOK_LOG"\nprintf '\\n' >> "$JIN_HOOK_LOG"\n`,
    { mode: 0o755 },
  )
  process.env["JIN_HOOK_LOG"] = logPath

  // Materialise the plugin the same way WritePlugin does, so the code under
  // test is byte-for-byte what ships.
  const source = readFileSync(join(import.meta.dir, "jin.ts"), "utf8")
  modulePath = join(workDir, "jin.ts")
  writeFileSync(modulePath, source.replaceAll("__JIN_BIN__", JSON.stringify(stubPath)))
})

afterAll(() => rmSync(workDir, { recursive: true, force: true }))

/** Events recorded by the stub so far, as "sessionID:EventName" pairs. */
function recorded(): string[] {
  if (!existsSync(logPath)) return []
  return readFileSync(logPath, "utf8")
    .split("\n")
    .filter((line) => line.trim())
    .map((line) => {
      const payload = JSON.parse(line)
      return `${payload.session_id}:${payload.hook_event_name}`
    })
}

/**
 * Boots one plugin instance and feeds it events, returning what the stub
 * received. `rootEnv` stands in for the JIN_OPENCODE_ROOT_SESSION that
 * SpawnCommand sets when resuming.
 */
type RunOptions = {
  /** Stands in for JIN_OPENCODE_ROOT_SESSION, which SpawnCommand sets on resume. */
  rootEnv?: string
  /** Sessions opencode would report, by id, for the SDK lookup. */
  known?: Record<string, { parentID?: string }>
  /** Makes every SDK lookup fail, as a wedged or absent server would. */
  lookupFails?: boolean
}

async function run(events: any[], options: RunOptions = {}): Promise<string[]> {
  writeFileSync(logPath, "")
  process.env["JIN_SESSION_ID"] = "jin-test-session"
  if (options.rootEnv) process.env["JIN_OPENCODE_ROOT_SESSION"] = options.rootEnv
  else delete process.env["JIN_OPENCODE_ROOT_SESSION"]

  lookupCount = 0
  const client = {
    session: {
      get: async ({ path }: any) => {
        lookupCount++
        if (options.lookupFails) throw new Error("server unavailable")
        const info = options.known?.[path.id]
        return info ? { data: { id: path.id, ...info } } : { data: undefined }
      },
    },
  }

  const mod = await import(modulePath)
  const hooks = await mod.server({ client })
  for (const event of events) await hooks.event({ event })
  return recorded()
}

/** Number of SDK lookups the most recent run() performed. */
let lookupCount = 0

const ROOT = "ses_root0000000000000000000"
const CHILD = "ses_child000000000000000000"

const created = (id: string, parentID?: string) => ({
  type: "session.created",
  properties: { sessionID: id, info: { id, ...(parentID ? { parentID } : {}) } },
})
const status = (id: string, type: string) => ({
  type: "session.status",
  properties: { sessionID: id, status: { type } },
})
const idle = (id: string) => ({ type: "session.idle", properties: { sessionID: id } })

test("a fresh root session reports SessionStart, then thinking, then Stop", async () => {
  expect(
    await run([created(ROOT), status(ROOT, "busy"), idle(ROOT)]),
  ).toEqual([`${ROOT}:SessionStart`, `${ROOT}:UserPromptSubmit`, `${ROOT}:Stop`])
})

test("repeated busy status collapses to one UserPromptSubmit", async () => {
  // opencode publishes session.status once per step — ~9 times for a
  // trivial turn. Forwarding each would multiply daemon IPC for no gain.
  expect(
    await run([
      created(ROOT),
      status(ROOT, "busy"),
      status(ROOT, "busy"),
      status(ROOT, "busy"),
      idle(ROOT),
    ]),
  ).toEqual([`${ROOT}:SessionStart`, `${ROOT}:UserPromptSubmit`, `${ROOT}:Stop`])
})

test("going idle reports Stop exactly once despite the paired status event", async () => {
  // SessionStatus.set publishes session.status{idle} AND session.idle.
  expect(
    await run([created(ROOT), status(ROOT, "busy"), status(ROOT, "idle"), idle(ROOT)]),
  ).toEqual([`${ROOT}:SessionStart`, `${ROOT}:UserPromptSubmit`, `${ROOT}:Stop`])
})

test("subagent sessions are never reported", async () => {
  // A child's SessionStart would re-key jind-ai's AgentSessionID onto the
  // subagent and break resume; its idle would end the parent's turn early.
  const sent = await run([
    created(ROOT),
    status(ROOT, "busy"),
    created(CHILD, ROOT),
    status(CHILD, "busy"),
    idle(CHILD),
    idle(ROOT),
  ])
  expect(sent).toEqual([`${ROOT}:SessionStart`, `${ROOT}:UserPromptSubmit`, `${ROOT}:Stop`])
})

test("an unannounced subagent is resolved via the SDK and ignored", async () => {
  // The task tool continues an existing subagent via task_id, which skips
  // Session.create and therefore publishes no session.created. Reporting
  // such an id is exactly the re-key bug, so it must be classified, not
  // assumed.
  const sent = await run([created(ROOT), status(CHILD, "busy"), idle(CHILD)], {
    known: { [CHILD]: { parentID: ROOT } },
  })
  expect(sent).toEqual([`${ROOT}:SessionStart`])
})

test("switching to an existing session is resolved via the SDK and followed", async () => {
  // /sessions, /resume, /continue and <leader>l all move to an existing
  // session without publishing session.created. Assuming "not a root"
  // there would silently freeze status after every switch.
  const OTHER = "ses_switched0000000000000000"
  expect(
    await run([status(OTHER, "busy"), idle(OTHER)], { known: { [OTHER]: {} } }),
  ).toEqual([`${OTHER}:UserPromptSubmit`, `${OTHER}:Stop`])
})

test("a classified session costs exactly one lookup", async () => {
  const OTHER = "ses_switched0000000000000000"
  await run([status(OTHER, "busy"), status(OTHER, "idle"), idle(OTHER)], {
    known: { [OTHER]: {} },
  })
  expect(lookupCount).toBe(1)
})

test("a failed lookup reports nothing rather than guessing", async () => {
  const OTHER = "ses_switched0000000000000000"
  expect(await run([status(OTHER, "busy"), idle(OTHER)], { lookupFails: true })).toEqual([])
})

test("overlapping handlers for one unknown id share a single lookup", async () => {
  // opencode dispatches this hook with `void hook.event?.(...)`, so handlers
  // overlap. Caching only the resolved answer would let the ~9 busy events of
  // one turn each fire their own lookup before the first came back.
  const OTHER = "ses_switched0000000000000000"
  writeFileSync(logPath, "")
  process.env["JIN_SESSION_ID"] = "jin-test-session"
  delete process.env["JIN_OPENCODE_ROOT_SESSION"]

  lookupCount = 0
  let release: (v: any) => void = () => {}
  const gate = new Promise((resolve) => (release = resolve))
  const client = {
    session: {
      get: async ({ path }: any) => {
        lookupCount++
        await gate
        return { data: { id: path.id } }
      },
    },
  }

  const mod = await import(modulePath)
  const hooks = await mod.server({ client })
  // Fire without awaiting, the way opencode does.
  const inFlight = [
    hooks.event({ event: status(OTHER, "busy") }),
    hooks.event({ event: status(OTHER, "busy") }),
    hooks.event({ event: status(OTHER, "busy") }),
  ]
  release(undefined)
  await Promise.all(inFlight)

  expect(lookupCount).toBe(1)
})

test("ids known from env or session.created never hit the SDK", async () => {
  await run([created(ROOT), status(ROOT, "busy"), idle(ROOT)])
  expect(lookupCount).toBe(0)
  await run([status(ROOT, "busy"), idle(ROOT)], { rootEnv: ROOT })
  expect(lookupCount).toBe(0)
})

test("resume follows the pinned root without any session.created", async () => {
  // `opencode --session <id>` republishes no session.created, so the only
  // thing marking the root is JIN_OPENCODE_ROOT_SESSION from SpawnCommand.
  expect(await run([status(ROOT, "busy"), idle(ROOT)], { rootEnv: ROOT })).toEqual([
    `${ROOT}:UserPromptSubmit`,
    `${ROOT}:Stop`,
  ])
})

test("resume still ignores subagents continued by task_id", async () => {
  // The combination that makes the deny-list version unsound: resumed
  // session (no created) plus a task_id-continued child (also no created).
  expect(await run([status(CHILD, "busy"), idle(CHILD), idle(ROOT)], { rootEnv: ROOT })).toEqual([
    `${ROOT}:Stop`,
  ])
})

test("/new mid-session is adopted as the new root", async () => {
  const OTHER = "ses_second00000000000000000"
  expect(
    await run([status(ROOT, "busy"), created(OTHER), idle(OTHER)], { rootEnv: ROOT }),
  ).toEqual([`${ROOT}:UserPromptSubmit`, `${OTHER}:SessionStart`, `${OTHER}:Stop`])
})

test("permission events map to permission and back to thinking", async () => {
  expect(
    await run([
      created(ROOT),
      { type: "permission.asked", properties: { sessionID: ROOT } },
      { type: "permission.replied", properties: { sessionID: ROOT } },
    ]),
  ).toEqual([`${ROOT}:SessionStart`, `${ROOT}:PermissionRequest`, `${ROOT}:UserPromptSubmit`])
})

test("an error without a sessionID is attributed to the current root", async () => {
  // session.error's sessionID is optional in opencode's schema; dropping
  // those would make StopFailure unreachable for some real failures.
  expect(
    await run([created(ROOT), { type: "session.error", properties: { error: {} } }]),
  ).toEqual([`${ROOT}:SessionStart`, `${ROOT}:StopFailure`])
})

test("noise on the bus is ignored", async () => {
  expect(
    await run([
      created(ROOT),
      { type: "message.part.delta", properties: { sessionID: ROOT } },
      { type: "plugin.added", properties: { id: "some-plugin" } },
      { type: "catalog.updated", properties: {} },
      { type: "session.diff", properties: { sessionID: ROOT, diff: [] } },
    ]),
  ).toEqual([`${ROOT}:SessionStart`])
})

test("the plugin contributes nothing outside a jind-ai session", async () => {
  delete process.env["JIN_SESSION_ID"]
  const mod = await import(modulePath)
  expect(await mod.server({})).toEqual({})
  process.env["JIN_SESSION_ID"] = "jin-test-session"
})

test("shell.env propagates the jind-ai session id", async () => {
  process.env["JIN_SESSION_ID"] = "jin-test-session"
  const mod = await import(modulePath)
  const hooks = await mod.server({})
  const output: { env: Record<string, string> } = { env: {} }
  await hooks["shell.env"]({}, output)
  expect(output.env["JIN_SESSION_ID"]).toBe("jin-test-session")
  // A future opencode that stops passing env must not throw on the
  // agent's shell path.
  await hooks["shell.env"]({}, {})
})

/**
 * The nested guard, tested as a round trip rather than as two halves: what
 * shell.env writes is fed back through the environment server() reads. Checked
 * apart, the mark and the check can drift — writing "yes" while testing for
 * "1" passes both halves and silences nothing in production.
 *
 * Run against a resumed parent and a fresh one, because each is the whole story
 * for a different failure: only a resumed parent has an id a nested session
 * could destroy, and only a fresh one covers the first spawn of every session.
 * Fixing one of these tests to the other's environment leaves that half blind.
 */
const withNestedEnv = async (
  opts: { root?: string; extra?: Record<string, string> },
  body: () => Promise<void>,
) => {
  process.env["JIN_SESSION_ID"] = "jin-test-session"
  if (opts.root) process.env["JIN_OPENCODE_ROOT_SESSION"] = opts.root
  else delete process.env["JIN_OPENCODE_ROOT_SESSION"]
  delete process.env["JIN_OPENCODE_NESTED"]
  Object.assign(process.env, opts.extra ?? {})
  try {
    await body()
  } finally {
    delete process.env["JIN_OPENCODE_NESTED"]
    delete process.env["JIN_OPENCODE_ROOT_SESSION"]
  }
}

for (const root of [undefined, "ses_parent"]) {
  const parent = root ? "a resumed parent" : "a fresh parent"

  test(`the mark shell.env writes is one server() refuses to run under (${parent})`, async () => {
    await withNestedEnv({ root }, async () => {
      const mod = await import(modulePath)
      const output: { env: Record<string, string> } = { env: {} }
      await (await mod.server({}))["shell.env"]({}, output)

      // Exactly what the child receives, mark included — or not, if none was set.
      Object.assign(process.env, output.env)
      expect(Object.keys(await mod.server({}))).toEqual([])
    })
  })

  test(`any truthy mark silences the plugin, not only the one it writes (${parent})`, async () => {
    await withNestedEnv({ root, extra: { JIN_OPENCODE_NESTED: "yes" } }, async () => {
      const mod = await import(modulePath)
      expect(Object.keys(await mod.server({}))).toEqual([])
    })
  })
}

test("session.deleted forgets the session", async () => {
  // Without cleanup the allow-list and suppression cache grow for the life
  // of the opencode process.
  const sent = await run([
    created(ROOT),
    { type: "session.deleted", properties: { sessionID: ROOT } },
    status(ROOT, "busy"),
  ], { lookupFails: true })
  expect(sent).toEqual([`${ROOT}:SessionStart`])
})
