import fs from "node:fs"
import { spawn } from "node:child_process"

const R = "/Users/aleph/Projects/telegram-mcp-go"
const DAG = "/Users/aleph/.bun/install/global/node_modules/omo-ai/plugin/runtime/dag"
const STATE_FILE = R + "/.omo/go-port/driver2-state.json"
const RUN1 = "dag_bad8408c-57e9-4cab-9645-b1b5da3106ff"
const STUCK_MS = 15 * 60 * 1000
const DAG_DIR = R + "/.omo/dags"
const DAG_FILE = {
  "launch-run2": "go-port-runtime.json",
  "launch-run3": "go-port-tools.json",
  "launch-run4": "go-port-cutover.json",
}

// Run-1 verify-foundation is report-only (writes no verdict file), so the driver
// re-runs the six foundation gates itself. Each gate returns {name, ok, out}; all must pass.
async function runFoundationGates() {
  const c = async (cmd) => {
    const x = await tool.bash({ command: "cd " + R + " && " + cmd + " 2>&1; echo EXIT:$?" })
    let t = ((x && x.text ? x.text : "") + "").trimEnd()
    const m = t.match(/EXIT:(\d+)$/)
    const exit = m ? m[1] : "?"
    if (m) t = t.slice(0, m.index)
    return { ok: exit === "0", out: t.slice(-400) }
  }
  const g = []
  const build = await c("go build ./...")
  g.push({ name: "go build", ok: build.ok, out: build.out })
  const vet = await c("go vet ./...")
  g.push({ name: "go vet", ok: vet.ok, out: vet.out })
  const test = await c("go test ./... -count=1")
  g.push({ name: "go test", ok: test.ok, out: test.out })
  const inv = await c(`node -e 'const i=require("./.omo/go-port/parity/inventory.json"); process.exit(i.total===132&&i.tools.length===132?0:3)'`)
  g.push({ name: "inventory=132", ok: inv.ok, out: inv.out })
  const pathGate = await c(`grep -rl 'Path traversal is not allowed\\.' internal/ | head -1`)
  g.push({ name: "path-gate present", ok: pathGate.out.trim().length > 0, out: pathGate.out })
  const stOut = await c("git status --porcelain")
  const stLines = stOut.out.split("\n").filter(l => l.trim())
  const stray = stLines.filter(l => {
    const s = l.trim()
    const path = s.length >= 3 ? s.slice(3) : s
    const allowed = path.startsWith(".omo/") || /^go\./.test(path) || path.startsWith("cmd/") || path.startsWith("internal/") || path.startsWith("AGENTS")
    if (s.startsWith("?? ")) return !allowed
    return true
  })
  g.push({ name: "scope-clean", ok: stray.length === 0, out: stray.slice(0, 5).join(" | ") })
  return g
}

const VERDICT = {
  "launch-run2": "verify-runtime-verdict.md",
  "launch-run3": "verify-tools-verdict.md",
  "launch-run4": "verify-cutover-verdict.md",
}
const NEXT = {
  "launch-run2": "launch-run3",
  "launch-run3": "launch-run4",
  "launch-run4": "release",
}

function persist(phase, extra) {
  const st = extra || {}
  st.phase = phase
  fs.writeFileSync(STATE_FILE, JSON.stringify(st, null, 1))
  return st
}
function loadState() {
  try { return JSON.parse(fs.readFileSync(STATE_FILE, "utf8")) } catch { return { phase: "fix-foundation" } }
}
function settled(snap) { return snap && snap.status && snap.status !== "running" }
function runningIds(snap) { return ((snap && snap.nodes) || []).filter(n => n.state === "running").map(n => n.id) }
function eligibleRetry(snap) { return ((snap && snap.nodes) || []).filter(n => n.state === "failed" || n.state === "cancelled").map(n => n.id) }
function nodeLastAct(snap, id) {
  const n = ((snap && snap.nodes) || []).find(x => x.id === id)
  const t = n && (n.lastActivityAt || n.updatedAt || n.lastAct)
  if (typeof t === "number") return t
  if (typeof t === "string") { const p = Date.parse(t); return isNaN(p) ? 0 : p }
  return 0
}

function detectQuota(snap) {
  const nodes = (snap && snap.nodes) || []
  for (const n of nodes) {
    const msg = ((n && n.error && (n.error.message || "")) + "")
    if (!msg || !/quota_limit|429/.test(msg)) continue
    let minutes = 30
    let m = msg.match(/(\d+)\s*h\s*(\d+)\s*m/)
    if (m) minutes = parseInt(m[1], 10) * 60 + parseInt(m[2], 10)
    else {
      m = msg.match(/(\d+)\s*m(?:in)?/)
      if (m) minutes = parseInt(m[1], 10)
    }
    return { blocked: true, resetAt: Date.now() + (minutes + 5) * 60000, evidence: msg.slice(0, 200) }
  }
  return { blocked: false }
}

async function snapshot(runId) {
  const sdk = await import(DAG + "/sdk.js")
  const r = await sdk.snapshot(runId)
  const d = r && r.details
  if (d && d.kind === "error") throw new Error("dag snapshot error: " + JSON.stringify(d.error || d))
  return (d && d.snapshot) || r
}

async function fixRun1(st) {
  const snap = await snapshot(RUN1)
  if (!settled(snap)) {
    const run = runningIds(snap)
    const stuck = run.filter(id => nodeLastAct(snap, id) > 0 && Date.now() - nodeLastAct(snap, id) > STUCK_MS)
    if (stuck.length) {
      const sdk = await import(DAG + "/sdk.js")
      for (const id of stuck) { try { await sdk.retry(RUN1, id) } catch (e) {} }
      return persist("fix-foundation", Object.assign({}, st, { note: "force-retried stuck nodes: " + stuck.join(",") }))
    }
    return { phase: "fix-foundation", note: "run1 in progress: " + (run.join(",") || snap.status) + " — monitor re-wakes" }
  }
  const eligible = eligibleRetry(snap)
  if (eligible.length) {
    const q = detectQuota(snap)
    if (q.blocked) {
      return persist("fix-foundation", Object.assign({}, st, { quotaResetAt: q.resetAt, quotaEvidence: q.evidence, note: "quota wall: backoff " + Math.round((q.resetAt - Date.now()) / 60000) + "m" }))
    }
    const sdk = await import(DAG + "/sdk.js")
    await sdk.retry(RUN1)
    return persist("fix-foundation", Object.assign({}, st, { lastRetryAt: Date.now(), note: "retry-all-eligible fired: " + eligible.join(",") }))
  }
  const nodes = (snap && snap.nodes) || []
  const byId = new Map(nodes.map(n => [n.id, n]))
  const ready = nodes.filter(n => n.state === "skipped" && (n.dependsOn || []).every(d => { const dep = byId.get(d); return dep && dep.state === "completed" }))
  if (ready.length) {
    const sdk = await import(DAG + "/sdk.js")
    await sdk.retry(RUN1, ready.map(n => n.id))
    return persist("fix-foundation", Object.assign({}, st, { lastRetryAt: Date.now(), note: "resumed skipped nodes with completed deps: " + ready.map(n => n.id).join(",") }))
  }
  const gates = await runFoundationGates()
  const bad = gates.filter(x => !x.ok)
  if (bad.length === 0) {
    return persist("launch-run2", Object.assign({}, st, { verifyRun1: "green", gateCheckAt: Date.now() }))
  }
  return persist("fix-foundation", Object.assign({}, st, { escalate: "foundation gates red: " + bad.map(x => x.name + ":" + x.out.slice(0, 120)).join(" | ") }))
}

async function waitVerify(st, key) {
  let runId = st["runId_" + key]
  if (!runId) {
    const sdk = await import(DAG + "/sdk.js")
    const def = JSON.parse(fs.readFileSync(DAG_DIR + "/" + DAG_FILE[key], "utf8"))
    const h = await sdk.start(def)
    const launchSt = Object.assign({}, st, { note: "launched " + key + " -> " + h.run_id })
    launchSt["runId_" + key] = h.run_id
    persist("wait-" + key, launchSt)
    return { phase: "wait-" + key, note: "launched " + key + " -> " + h.run_id }
  }
  const snap = await snapshot(runId)
  if (!settled(snap)) {
    const run = runningIds(snap)
    const stuck = run.filter(id => nodeLastAct(snap, id) > 0 && Date.now() - nodeLastAct(snap, id) > STUCK_MS)
    if (stuck.length) {
      const sdk = await import(DAG + "/sdk.js")
      for (const id of stuck) { try { await sdk.retry(runId, id) } catch (e) {} }
      return persist("wait-" + key, Object.assign({}, st, { note: "force-retried stuck: " + stuck.join(",") }))
    }
    return { phase: "wait-" + key, note: "run " + key + " running: " + (run.join(",") || snap.status) }
  }
  const retryBudget = st["retryCount_" + key] || 0
  const eligible = eligibleRetry(snap)
  if (eligible.length) {
    const q = detectQuota(snap)
    if (q.blocked) {
      return persist("wait-" + key, Object.assign({}, st, { quotaResetAt: q.resetAt, quotaEvidence: q.evidence, note: "quota wall on " + key + ": backoff " + Math.round((q.resetAt - Date.now()) / 60000) + "m" }))
    }
  }
  if (eligible.length && retryBudget < 4) {
    const sdk = await import(DAG + "/sdk.js")
    await sdk.retry(runId)
    const retriedSt = Object.assign({}, st)
    retriedSt["retryCount_" + key] = retryBudget + 1
    retriedSt.lastRetryAt = Date.now()
    return persist("wait-" + key, Object.assign(retriedSt, { note: "retry-all-eligible: " + eligible.join(",") }))
  }
  const vtPath = R + "/.omo/go-port/" + VERDICT[key]
  let vt = ""
  try { vt = fs.readFileSync(vtPath, "utf8") } catch {}
  const failLines = vt ? vt.split("\n").filter(l => /^FAIL/.test(l)).length : -1
  // A documented orchestrator waiver (waiver-<key>.md) satisfies the verdict check
  // for this stage; node-level failures are still enforced above via `eligible`.
  const waived = failLines > 0 && fs.existsSync(R + "/.omo/go-port/waiver-" + key + ".md")
  if (eligible.length === 0 && (failLines === 0 || waived)) {
    const greenSt = Object.assign({}, st)
    greenSt["verify_" + key] = waived ? "waived" : "green"
    return persist(NEXT[key], greenSt)
  }
  return persist("wait-" + key, Object.assign({}, st, { note: key + " red: eligible=" + eligible.join(",") + " failLines=" + failLines + " — escalate" }))
}

function gitRun(cmd) {
  return new Promise(resolve => {
    const c = spawn("sh", ["-c", "cd " + R + " && " + cmd])
    let out = ""
    c.stdout.on("data", d => { out += d })
    c.stderr.on("data", d => { out += d })
    c.on("close", code => resolve({ ok: code === 0, out: out.slice(0, 500) }))
  })
}

const COMMIT_MSG = `Port telegram-mcp to Go (gogram + modelcontextprotocol/go-sdk)
- internal/config, internal/session, internal/kit, internal/mcpserver
- 9 tool-domain packages, 132 tools + offline tests
- gen-session + migrate-session CLIs, Go Dockerfile, README/AGENTS rewrite
- removed all Python source`

async function release(st) {
  // HARD GATE: never auto-commit/tag/push. Stop and wait for explicit user approval.
  return persist("awaiting-release", Object.assign({}, st, { note: "chain green; awaiting explicit user approval for git commit/tag/push (v0.1.0)" }))
}

export async function step() {
  const st = loadState()
  if (st.quotaResetAt && Date.now() < st.quotaResetAt) {
    return { phase: st.phase, note: "quota backoff until " + new Date(st.quotaResetAt).toISOString() + " (no tool calls made)" }
  }
  if (st.quotaResetAt) {
    delete st.quotaResetAt
    delete st.quotaEvidence
    persist(st.phase, st)
  }
  const key = st.phase.replace("wait-", "")
  switch (st.phase) {
    case "fix-foundation": return fixRun1(st)
    case "launch-run2":
    case "launch-run3":
    case "launch-run4":
    case "wait-launch-run2":
    case "wait-launch-run3":
    case "wait-launch-run4":
      return waitVerify(st, key)
    case "release": return release(st)
    case "awaiting-release": return { phase: "awaiting-release", note: "chain green; awaiting user approval for commit/tag/push" }
    case "done": return { phase: "done", note: "complete" }
    default: return { phase: st.phase, note: "unknown phase — inspect " + STATE_FILE }
  }
}
