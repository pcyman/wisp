import { mkdir, rename, unlink, writeFile } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { dirname } from "node:path";

// Pi v0.85.1 public extension events provide conservative busy/settled
// boundaries. Reporter output is advisory; Wisp verifies container liveness.
export default function wispAgentStatus(pi) {
  const path = process.env.WISP_AGENT_STATUS_PATH || "/run/wisp/agent-status/agent.json";
  let busy = false;
  let promptDepth = 0;
  let written;
  let queue = Promise.resolve();

  const enqueue = (next) => {
    queue = queue.then(async () => {
      const value = JSON.stringify(next);
      if (value === written) return;
      const temporary = `${path}.${process.pid}.${randomUUID()}.tmp`;
      try {
        await mkdir(dirname(path), { recursive: true });
        await writeFile(temporary, JSON.stringify({
          schema_version: 1,
          ...next,
          updated_at: new Date().toISOString(),
        }) + "\n", { flag: "wx", mode: 0o600 });
        await rename(temporary, path);
        written = value;
      } finally {
        await unlink(temporary).catch(() => {});
      }
    }).catch(() => {});
    return queue;
  };

  void enqueue({ state: "idle" });
  pi.on("session_start", () => {
    busy = false;
    promptDepth = 0;
    return enqueue({ state: "idle" });
  });
  pi.on("agent_start", () => {
    busy = true;
    return enqueue(promptDepth ? { state: "waiting", reason: "question" } : { state: "working" });
  });
  pi.on("agent_settled", () => {
    busy = false;
    return enqueue(promptDepth ? { state: "waiting", reason: "question" } : { state: "idle" });
  });
  pi.on("ui_prompt_start", () => {
    promptDepth++;
    return enqueue({ state: "waiting", reason: "question" });
  });
  pi.on("ui_prompt_end", () => {
    promptDepth = Math.max(0, promptDepth - 1);
    if (promptDepth) return enqueue({ state: "waiting", reason: "question" });
    return enqueue({ state: busy ? "working" : "idle" });
  });
  pi.on("session_shutdown", () => enqueue({ state: "idle" }));
}
