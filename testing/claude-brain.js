// claude-brain: a REAL brain for `ask` that reuses the local Claude CLI the same
// way OpenClaw's cli-backend does — by shelling out to `claude -p` (a sanctioned
// subprocess of Anthropic's own client), NOT by reusing the OAuth token against
// api.anthropic.com. It speaks `ask`'s /v1/messages tool-use protocol on one
// side and drives `claude -p` on the other, so it's drop-in:
//
//   node testing/claude-brain.js                 # listens on :4012
//   ask --base-url http://localhost:4012 aws "list my buckets"   # no API key
//
// Requires: `claude` on PATH, already logged in (`claude auth status`).
const http = require("http");
const { execFileSync } = require("child_process");

const PORT = process.env.PORT || 4012;

// ask sends model ids like "claude-sonnet-4-6"; the CLI takes those or aliases.
function cliModel(m) {
  const s = String(m || "").toLowerCase();
  if (s.includes("opus")) return "opus";
  if (s.includes("haiku")) return "haiku";
  return "sonnet";
}

// Flatten ask's conversation (text / tool_use / tool_result blocks) into a plain
// transcript the CLI can read.
function render(messages) {
  const lines = [];
  for (const m of messages || []) {
    const content = Array.isArray(m.content) ? m.content : [{ type: "text", text: m.content }];
    for (const b of content) {
      if (b.type === "text") lines.push(`${m.role === "user" ? "REQUEST" : "NOTE"}: ${b.text}`);
      else if (b.type === "tool_use") lines.push(`RAN: ${JSON.stringify(b.input.args)}${b.input.profiles ? ` [profiles: ${JSON.stringify(b.input.profiles)}]` : ""}`);
      else if (b.type === "tool_result") {
        const txt = Array.isArray(b.content) ? b.content.map((c) => c.text || "").join("") : b.content;
        lines.push(`OUTPUT:\n${txt}`);
      }
    }
  }
  return lines.join("\n");
}

function askClaude(system, messages, model) {
  const prompt = [
    "You are the planning model inside a CLI agent loop. Follow the SYSTEM rules below.",
    "Decide the SINGLE next step from the transcript.",
    'Reply with ONE json object on the final line and nothing after it:',
    '  {"args":["..."],"profiles":["..."]}  to run the provider CLI (profiles optional), or',
    '  {"answer":"..."}                      when you can answer the original request.',
    "",
    "=== SYSTEM ===",
    system,
    "",
    "=== TRANSCRIPT ===",
    render(messages),
  ].join("\n");

  const out = execFileSync(
    "claude",
    ["-p", "--model", cliModel(model), "--output-format", "text",
     "--disallowedTools", "Bash,Read,Edit,Write,WebFetch,WebSearch"],
    { input: prompt, encoding: "utf8", maxBuffer: 10 * 1024 * 1024,
      env: { ...process.env, ANTHROPIC_API_KEY: "", ANTHROPIC_AUTH_TOKEN: "" } }
  );

  // Grab the last {...} JSON object in the reply.
  const matches = out.match(/\{[\s\S]*\}/g) || [];
  for (let i = matches.length - 1; i >= 0; i--) {
    try { return JSON.parse(matches[i]); } catch (_) {}
  }
  return { answer: out.trim() || "(no response from claude)" };
}

function envelope(model, content, stop) {
  return JSON.stringify({
    id: "msg_claudebrain", type: "message", role: "assistant",
    model: model || "claude-sonnet-4-6", content,
    stop_reason: stop, stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1 },
  });
}

http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    let j = {};
    try { j = JSON.parse(body); } catch (_) {}
    const system = (Array.isArray(j.system) ? j.system.map((s) => s.text).join("\n") : j.system) || "";
    console.error(`[claude-brain] requested model=${j.model || "(none)"} -> claude -p --model ${cliModel(j.model)}`);
    res.setHeader("content-type", "application/json");
    let decision;
    try {
      decision = askClaude(system, j.messages, j.model);
    } catch (e) {
      console.error("[claude-brain] error:", e.message);
      res.statusCode = 500;
      res.end(JSON.stringify({ type: "error", error: { type: "api_error", message: e.message } }));
      return;
    }
    if (decision && Array.isArray(decision.args)) {
      const input = { args: decision.args };
      if (Array.isArray(decision.profiles) && decision.profiles.length) input.profiles = decision.profiles;
      console.error(`[claude-brain] -> run ${JSON.stringify(decision.args)}`);
      res.end(envelope(j.model, [
        { type: "tool_use", id: "toolu_cb_" + Date.now(), name: "run_command", input },
      ], "tool_use"));
    } else {
      console.error(`[claude-brain] -> answer`);
      res.end(envelope(j.model, [{ type: "text", text: String(decision.answer || "") }], "end_turn"));
    }
  });
}).listen(PORT, "127.0.0.1", () => console.log(`claude-brain on http://127.0.0.1:${PORT} (model via local 'claude -p')`));
