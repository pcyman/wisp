import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import statusExtension from "./pi-agent-status.mjs";

test("Pi reporter emits opt-in startup timing markers", async () => {
  const root = await mkdtemp(join(tmpdir(), "wisp-pi-timing-"));
  const previousPath = process.env.WISP_AGENT_STATUS_PATH;
  const previousTiming = process.env.WISP_STARTUP_TIMING;
  const previousError = console.error;
  const messages = [];
  process.env.WISP_AGENT_STATUS_PATH = join(root, "agent.json");
  process.env.WISP_STARTUP_TIMING = "1";
  console.error = (...args) => messages.push(args.join(" "));
  const handlers = new Map();
  try {
    const timingExtension = (await import(`./pi-agent-status.mjs?timing-test=${Date.now()}`)).default;
    timingExtension({ on(name, handler) { handlers.set(name, handler); } });
    await handlers.get("session_start")({});
    assert.ok(messages.some((message) => message.includes("pi.status_extension.module_loaded")));
    assert.ok(messages.some((message) => message.includes("pi.status_extension.factory")));
    assert.ok(messages.some((message) => message.includes("pi.session_start")));
  } finally {
    console.error = previousError;
    if (previousPath === undefined) delete process.env.WISP_AGENT_STATUS_PATH;
    else process.env.WISP_AGENT_STATUS_PATH = previousPath;
    if (previousTiming === undefined) delete process.env.WISP_STARTUP_TIMING;
    else process.env.WISP_STARTUP_TIMING = previousTiming;
    await rm(root, { recursive: true, force: true });
  }
});

test("Pi reporter follows busy, prompt, and settled boundaries", async () => {
  const root = await mkdtemp(join(tmpdir(), "wisp-pi-status-"));
  const statusPath = join(root, "status", "agent.json");
  const previous = process.env.WISP_AGENT_STATUS_PATH;
  process.env.WISP_AGENT_STATUS_PATH = statusPath;
  const handlers = new Map();
  try {
    statusExtension({ on(name, handler) { handlers.set(name, handler); } });
    await handlers.get("session_start")({});
    assert.equal(JSON.parse(await readFile(statusPath, "utf8")).state, "idle");

    await handlers.get("agent_start")({});
    assert.equal(JSON.parse(await readFile(statusPath, "utf8")).state, "working");

    await handlers.get("ui_prompt_start")({ kind: "input" });
    const waiting = JSON.parse(await readFile(statusPath, "utf8"));
    assert.deepEqual({ state: waiting.state, reason: waiting.reason }, { state: "waiting", reason: "question" });

    await handlers.get("ui_prompt_end")({});
    assert.equal(JSON.parse(await readFile(statusPath, "utf8")).state, "working");

    await handlers.get("agent_settled")({});
    assert.equal(JSON.parse(await readFile(statusPath, "utf8")).state, "idle");
  } finally {
    if (previous === undefined) delete process.env.WISP_AGENT_STATUS_PATH;
    else process.env.WISP_AGENT_STATUS_PATH = previous;
    await rm(root, { recursive: true, force: true });
  }
});
