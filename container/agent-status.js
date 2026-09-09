import { mkdir, writeFile, rename, unlink } from "node:fs/promises";
import { dirname } from "node:path";
import { randomUUID } from "node:crypto";

// Verified against anomalyco/opencode v1.18.29 (16747470f976aca3d362ad730bcd3fe82ecc2c9a):
// src/plugin/index.ts, src/session/{status,processor}.ts, schema/src/v1/{session,permission,question}.ts.
// Event-only: no SDK calls during initialization (the host subscribes AFTER init).
// Resumed sessions need a session.created/updated event to establish root identity;
// unknown sessions contribute activity, but never a terminal result. No history replay.
// One instance/writer is required. This is advisory status, not a liveness probe.

function reduce(previous = { sessions: new Map(), sequence: 0, status: { state: "idle" } }, event) {
  const type = event?.type;
  const p = event?.properties ?? {};
  const info = p.info;
  const id = p.sessionID ?? info?.sessionID ?? info?.id;
  if (typeof id !== "string") return previous;
  const sessions = new Map(previous.sessions);
  let sequence = previous.sequence;
  const old = sessions.get(id) ?? { active: false, pending: new Set(), involved: false };
  const s = { ...old, pending: new Set(old.pending) };
  const statusEvent = type === "session.status" || type === "session.idle";
  const busy = type === "session.status" && ["busy", "retry"].includes(p.status?.type);
  const user = type === "message.updated" && info?.role === "user";
  const newUser = user && typeof info.id === "string" &&
    (!s.user || info.time?.created > s.user.created ||
      (info.time?.created === s.user.created && info.id > s.user.id));
  const asked = type === "permission.asked" || type === "question.asked";
  const start = (busy && !s.active) || newUser || asked;

  // A new activity wave cannot inherit another root's previous outcome.
  if (start && ![...sessions.values()].some((item) => item.active || item.pending.size)) {
    for (const [key, item] of sessions) {
      sessions.set(key, { ...item, involved: false, result: undefined, quiesced: undefined });
    }
    s.involved = false;
    s.result = undefined;
  }
  if (start) {
    s.involved = true;
    s.quiesced = undefined;
  }
  if (newUser || (busy && !s.active)) {
    s.result = undefined;
    s.blockedMessage = s.latest?.id;
  }

  if (type === "session.created" || type === "session.updated") {
    if (!info || info.id !== id) return previous;
    s.root = !info.parentID;
  } else if (type === "session.deleted") {
    sessions.delete(id);
  } else if (statusEvent) {
    if (!busy && type !== "session.idle" && p.status?.type !== "idle") return previous;
    s.active = busy;
    s.retry = p.status?.type === "retry";
    // Cancellation can remove pending requests without publishing replies.
    if (!busy) s.pending.clear();
  } else if (asked) {
    if (typeof p.id !== "string") return previous;
    s.pending.add(`${type.split(".")[0]}:${p.id}`);
  } else if (["permission.replied", "question.replied", "question.rejected"].includes(type)) {
    s.pending.delete(`${type.split(".")[0]}:${p.requestID}`);
  } else if (newUser) {
    s.user = { id: info.id, created: info.time?.created };
  } else if (type === "message.updated" && info?.role === "assistant") {
    if (info.summary || !s.involved || typeof info.id !== "string" ||
      (s.user && info.parentID !== s.user.id) || info.id === s.blockedMessage) return previous;
    const created = info.time?.created;
    if (!Number.isFinite(created)) return previous;
    if (s.latest && (created < s.latest.created ||
      (created === s.latest.created && info.id < s.latest.id))) return previous;
    s.latest = { id: info.id, created };
    s.result = undefined;
    if (Number.isFinite(info.time?.completed)) {
      if (info.error?.name === "MessageAbortedError") s.result = "idle";
      else if (info.error || info.finish === "error" || info.finish === "content-filter") s.result = "failed";
      else if (["stop", "length"].includes(info.finish)) s.result = "done";
    }
  } else {
    // session.error is also emitted for recoverable overflow and plugin errors.
    return previous;
  }
  // Rank the transition to quiescence, not message arrival or repeated idle events.
  // A completed error message may arrive after idle without changing this rank.
  if (type !== "session.deleted") {
    if (s.involved && s.quiesced === undefined && !s.active && !s.pending.size &&
      (statusEvent || old.pending.size)) s.quiesced = ++sequence;
    sessions.set(id, s);
  }
  const all = [...sessions.values()];
  const roots = all.filter((item) => item.involved && item.root === true);
  let status = { state: "idle" };
  if (all.some((item) => item.pending.size)) {
    const permission = all.some((item) => [...item.pending].some((key) => key.startsWith("permission:")));
    status = { state: "waiting", reason: permission ? "permission" : "question" };
  } else if (all.some((item) => item.active)) {
    status = all.some((item) => item.active && item.retry)
      ? { state: "working", reason: "retry" } : { state: "working" };
  } else if (!all.some((item) => item.involved && item.root === undefined) &&
    roots.length && roots.every((item) => item.quiesced !== undefined)) {
    const latest = roots.reduce((a, b) => a.quiesced > b.quiesced ? a : b);
    status = { state: latest.result ?? "idle" };
  }
  return { sessions, sequence, status };
}

export const AgentStatusPlugin = async () => {
  const path = process.env.WISP_AGENT_STATUS_PATH || "/run/wisp/agent-status/agent.json";
  let model = reduce();
  let written;
  let queue = Promise.resolve();
  const enqueue = (event) => {
    queue = queue.then(async () => {
      model = reduce(model, event);
      const value = JSON.stringify(model.status);
      if (value === written) return;
      const tmp = `${path}.${process.pid}.${randomUUID()}.tmp`;
      try {
        await mkdir(dirname(path), { recursive: true });
        await writeFile(tmp, JSON.stringify({
          schema_version: 1,
          ...model.status,
          updated_at: new Date().toISOString(),
        }) + "\n", { flag: "wx", mode: 0o600 });
        await rename(tmp, path);
        written = value;
      } finally {
        await unlink(tmp).catch(() => {});
      }
    }).catch(() => {}); // Status I/O must never reject a host callback or poison the queue.
    return queue;
  };
  // Do not delay plugin initialization or make reentrant client requests.
  void enqueue();
  return { event: ({ event } = {}) => enqueue(event), dispose: () => queue };
};

// Keep a single module export: the legacy loader invokes EVERY exported value.
// A function property exposes the pure reducer without registering another plugin.
AgentStatusPlugin.reduce = reduce;
