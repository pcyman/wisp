import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, readFile, readdir, rm, mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Node 18 treats .js as CJS without package.json; OpenCode loads it as ESM.
const source = await readFile(new URL("./agent-status.js", import.meta.url), "utf8");
const module = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
const { AgentStatusPlugin } = module;
const { reduce } = AgentStatusPlugin;
const event = (type, properties) => ({ type, properties });
const session = (id = "root", parentID) => event("session.created", { info: { id, parentID } });
const status = (type, sessionID = "root") => event("session.status", { sessionID, status: { type } });
const user = (id = "u1", created = 1, sessionID = "root") => event("message.updated", {
  info: { id, sessionID, role: "user", time: { created } },
});
const assistant = (overrides = {}) => event("message.updated", { info: {
  id: "a1", sessionID: "root", parentID: "u1", role: "assistant",
  time: { created: 2, completed: 3 }, finish: "stop", ...overrides,
} });
const run = (...events) => events.reduce(reduce, reduce());
const started = () => run(session(), user(), status("busy"));

test("single loader-safe export; pure reducer does not mutate input", () => {
  assert.deepEqual(Object.keys(module), ["AgentStatusPlugin"]);
  const before = started();
  const copy = structuredClone(before);
  reduce(before, event("permission.asked", { sessionID: "root", id: "p" }));
  assert.deepEqual(before, copy);
});

test("root completion requires completed message and global quiescence", () => {
  let s = started();
  s = reduce(s, assistant({ time: { created: 2 } }));
  assert.equal(s.status.state, "working");
  s = reduce(s, status("idle"));
  assert.equal(s.status.state, "idle");
  s = reduce(s, assistant());
  assert.equal(s.status.state, "done");
  assert.equal(reduce(s, event("session.idle", { sessionID: "root" })).status.state, "done");
});

test("child results cannot finish or fail root; child activity delays root completion", () => {
  for (const error of [undefined, { name: "APIError" }]) {
    let s = started();
    s = reduce(s, session("child", "root"));
    s = reduce(s, status("busy", "child"));
    s = reduce(s, assistant({ sessionID: "child", error }));
    s = reduce(s, status("idle", "child"));
    assert.equal(s.status.state, "working");
    assert.equal(reduce(s, status("idle")).status.state, "idle");
    s = reduce(s, status("busy", "child"));
    s = reduce(s, assistant());
    s = reduce(s, status("idle"));
    assert.equal(s.status.state, "working");
    assert.equal(reduce(s, status("idle", "child")).status.state, "done");
  }
});

test("pending permission/question IDs aggregate across sessions and request kinds", () => {
  let s = started();
  s = reduce(s, session("child", "root"));
  for (const [type, sessionID] of [["permission", "root"], ["question", "child"]]) {
    s = reduce(s, event(`${type}.asked`, { sessionID, id: "same" }));
  }
  assert.deepEqual(s.status, { state: "waiting", reason: "permission" });
  s = reduce(s, event("permission.replied", { sessionID: "root", requestID: "same", reply: "reject" }));
  assert.deepEqual(s.status, { state: "waiting", reason: "question" });
  s = reduce(s, event("question.rejected", { sessionID: "child", requestID: "same" }));
  assert.equal(s.status.state, "working");
  s = reduce(s, event("question.asked", { sessionID: "root", id: "q" }));
  s = reduce(s, event("question.replied", { sessionID: "root", requestID: "q" }));
  assert.equal(s.status.state, "working");
});

test("retry and session.error are not definitive; completed root errors are", () => {
  let s = started();
  s = reduce(s, event("session.error", { sessionID: "root", error: { name: "APIError" } }));
  s = reduce(s, status("retry"));
  assert.deepEqual(s.status, { state: "working", reason: "retry" });
  assert.deepEqual(reduce(s, status("busy")).status, { state: "working" });
  s = reduce(s, status("idle"));
  assert.equal(s.status.state, "idle");
  s = reduce(s, assistant({ error: { name: "APIError", data: { message: "SECRET" } } }));
  assert.deepEqual(s.status, { state: "failed" });
});

test("cancellation is idle and clears unanswered requests", () => {
  let s = started();
  s = reduce(s, event("question.asked", { sessionID: "root", id: "q" }));
  s = reduce(s, assistant({ error: { name: "MessageAbortedError" } }));
  assert.equal(s.status.state, "waiting");
  s = reduce(s, status("idle"));
  assert.deepEqual(s.status, { state: "idle" });
});

test("new turns and busy-only runs never reuse prior completions", () => {
  for (const next of [user("u2", 4), status("busy")]) {
    let s = reduce(reduce(started(), assistant()), status("idle"));
    s = reduce(s, next);
    s = reduce(s, assistant()); // Late metadata update from previous turn.
    s = reduce(s, status("idle"));
    assert.equal(s.status.state, "idle");
  }
  let s = reduce(reduce(started(), assistant()), status("idle"));
  s = reduce(s, user("u2", 4));
  s = reduce(s, status("busy"));
  s = reduce(s, user()); // Old user summary update must not reset the turn.
  s = reduce(s, assistant({ id: "a2", parentID: "u2", time: { created: 5, completed: 6 } }));
  assert.equal(reduce(s, status("idle")).status.state, "done");
});

test("intermediate, summary, and unknown finishes are not success", () => {
  for (const fields of [{ finish: "tool-calls" }, { finish: "unknown" }, { finish: undefined }, { summary: true }]) {
    assert.equal(reduce(reduce(started(), assistant(fields)), status("idle")).status.state, "idle");
  }
  for (const finish of ["stop", "length", "error", "content-filter"]) {
    assert.equal(reduce(reduce(started(), assistant({ finish })), status("idle")).status.state,
      ["stop", "length"].includes(finish) ? "done" : "failed");
  }
});

test("newer assistant steps supersede results and late older messages cannot restore them", () => {
  let s = reduce(started(), assistant());
  s = reduce(s, assistant({ id: "a2", time: { created: 4 }, finish: undefined }));
  s = reduce(s, assistant());
  s = reduce(s, status("idle"));
  assert.equal(s.status.state, "idle");
  s = reduce(s, assistant({ id: "a2", time: { created: 4, completed: 5 }, error: { name: "APIError" } }));
  assert.equal(s.status.state, "failed");
});

test("same request IDs in separate sessions do not collide", () => {
  let s = started();
  for (const sessionID of ["root", "child"]) {
    s = reduce(s, event("permission.asked", { sessionID, id: "p" }));
  }
  s = reduce(s, event("permission.replied", { sessionID: "root", requestID: "p" }));
  assert.equal(s.status.state, "waiting");
  s = reduce(s, event("session.deleted", { info: { id: "child" } }));
  assert.equal(s.status.state, "working");
});

test("unknown ancestry is conservative; session updates establish identity; deletion clears activity", () => {
  let s = run(status("busy"), assistant(), status("idle"));
  assert.equal(s.status.state, "idle");
  s = reduce(s, event("session.updated", { info: { id: "root", parentID: "parent" } }));
  assert.equal(s.status.state, "idle");
  s = reduce(s, status("busy"));
  s = reduce(s, event("permission.asked", { sessionID: "root", id: "p" }));
  s = reduce(s, event("session.deleted", { info: { id: "root" } }));
  assert.equal(s.status.state, "idle");
  assert.equal(s.sessions.size, 0);
});

test("multiple roots aggregate; a new wave forgets old failures", () => {
  let s = started();
  s = reduce(s, session("other"));
  s = reduce(s, status("busy", "other"));
  s = reduce(s, assistant({ error: { name: "APIError" } }));
  s = reduce(s, status("idle"));
  assert.equal(s.status.state, "working");
  s = reduce(s, assistant({ sessionID: "other" }));
  s = reduce(s, status("idle", "other"));
  assert.equal(s.status.state, "done");
  s = reduce(s, status("busy", "other"));
  s = reduce(s, assistant({ sessionID: "other", id: "a2", time: { created: 4, completed: 5 } }));
  s = reduce(s, status("idle", "other"));
  assert.equal(s.status.state, "done");
});

test("overlapping roots use latest quiescence, not failure priority or message arrival", () => {
  for (const [first, last, expected] of [
    [{ error: { name: "APIError" } }, {}, "done"],
    [{}, { error: { name: "APIError" } }, "failed"],
    [{ error: { name: "APIError" } }, { error: { name: "MessageAbortedError" } }, "idle"],
    [{}, { finish: "tool-calls" }, "idle"],
  ]) {
    let s = started();
    s = reduce(s, session("other"));
    s = reduce(s, status("busy", "other"));
    s = reduce(s, status("idle"));
    s = reduce(s, assistant({ sessionID: "other", ...last }));
    s = reduce(s, status("idle", "other"));
    assert.deepEqual(s.status, { state: expected });
    // The older root's completed message arrives last, but cannot win the rank.
    s = reduce(s, assistant(first));
    assert.deepEqual(s.status, { state: expected });
    const sequence = s.sequence;
    for (const sessionID of ["other", "root", "root"]) {
      s = reduce(s, status("idle", sessionID));
      s = reduce(s, event("session.idle", { sessionID }));
      assert.equal(s.sequence, sequence);
      assert.deepEqual(s.status, { state: expected });
    }
  }
});

test("new user during active work clears only that root's outcome and rejects stale messages", () => {
  for (const idleBeforeUser of [false, true]) {
    let s = started();
    s = reduce(s, session("other"));
    s = reduce(s, status("busy", "other"));
    s = reduce(s, assistant({ error: { name: "APIError" } }));
    s = reduce(s, assistant({ sessionID: "other" }));
    if (idleBeforeUser) s = reduce(s, status("idle"));
    s = reduce(s, user("u2", 4));
    assert.equal(s.sessions.get("root").result, undefined);
    assert.equal(s.sessions.get("root").quiesced, undefined);
    assert.equal(s.sessions.get("other").result, "done");
    s = reduce(s, status("idle", "other"));
    s = reduce(s, status("idle"));
    assert.deepEqual(s.status, { state: "idle" });
    s = reduce(s, assistant({ error: { name: "APIError" } }));
    // Even an unseen, later-created message from the old parent is stale.
    s = reduce(s, assistant({ id: "a9", time: { created: 9, completed: 10 } }));
    assert.deepEqual(s.status, { state: "idle" });
    s = reduce(s, assistant({ id: "a2", parentID: "u2", time: { created: 5, completed: 6 } }));
    assert.deepEqual(s.status, { state: "done" });
  }
});

test("unknown involved ancestry suppresses terminal outcomes from known roots", () => {
  for (const error of [undefined, { name: "APIError" }]) {
    let s = started();
    s = reduce(s, status("busy", "unknown"));
    s = reduce(s, status("idle", "unknown"));
    s = reduce(s, assistant({ error }));
    s = reduce(s, status("idle"));
    assert.deepEqual(s.status, { state: "idle" });
    s = reduce(s, session("unknown", "root"));
    assert.deepEqual(s.status, { state: error ? "failed" : "done" });
  }
});

test("plugin serializes callbacks, writes only schema fields atomically, and recovers from I/O failure", async () => {
  const dir = await mkdtemp(join(tmpdir(), "wisp-status-"));
  const oldPath = process.env.WISP_AGENT_STATUS_PATH;
  try {
    const path = join(dir, "status", "agent.json");
    process.env.WISP_AGENT_STATUS_PATH = path;
    // Parent initially is a file, so even the initialization write fails.
    await writeFile(join(dir, "status"), "blocked");
    const plugin = await AgentStatusPlugin({ client: new Proxy({}, { get() { throw new Error("No SDK calls"); } }) });
    await plugin.dispose();
    await rm(join(dir, "status"));
    await mkdir(join(dir, "status"));
    await plugin.event({ event: session() });
    const initial = JSON.parse(await readFile(path, "utf8"));
    assert.equal(initial.state, "idle");
    const events = [user(), status("busy"), assistant({ secret: "SECRET" }), status("idle")];
    let reading = true;
    const reader = (async () => {
      while (reading) {
        const value = JSON.parse(await readFile(path, "utf8"));
        assert.equal(value.schema_version, 1);
        assert.ok(["idle", "working", "done"].includes(value.state));
      }
    })();
    try {
      await Promise.all(events.map((event) => plugin.event({ event })));
    } finally {
      reading = false;
      await reader;
    }
    await plugin.dispose();
    const text = await readFile(path, "utf8");
    const value = JSON.parse(text);
    assert.equal(value.state, "done");
    assert.equal(new Date(value.updated_at).toISOString(), value.updated_at);
    assert.deepEqual(Object.keys(value).sort(), ["schema_version", "state", "updated_at"]);
    assert.ok(!text.includes("SECRET"));
    assert.deepEqual(await readdir(join(dir, "status")), ["agent.json"]);

    // A rename failure must remove its temporary file and allow a same-state retry.
    await rm(path);
    await mkdir(path);
    await plugin.event({ event: status("busy") });
    assert.deepEqual(await readdir(join(dir, "status")), ["agent.json"]);
    await rm(path, { recursive: true });
    await plugin.event({ event: event("session.error", { sessionID: "root" }) });
    assert.equal(JSON.parse(await readFile(path, "utf8")).state, "working");
  } finally {
    if (oldPath === undefined) delete process.env.WISP_AGENT_STATUS_PATH;
    else process.env.WISP_AGENT_STATUS_PATH = oldPath;
    await rm(dir, { recursive: true, force: true });
  }
});
