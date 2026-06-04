// Minimal Anthropic Messages API mock for end-to-end testing of `ask` WITHOUT a
// real Anthropic key. It is SCRIPTED, not intelligent: it picks a fixed command
// from keywords in the prompt, returns one run_command tool_use, then an
// end_turn summary once the tool_result comes back. Use it to exercise the
// plumbing (loop / classifier / confirmation gate / fan-out / transparency).
// Real natural-language translation requires the real model (drop --base-url).
//
//   node testing/anthropic-mock.js          # listens on http://127.0.0.1:4009
const http = require("http");
const PORT = process.env.PORT || 4009;

function message(model, content, stop) {
  return JSON.stringify({
    id: "msg_mock_0001", type: "message", role: "assistant",
    model: model || "claude-sonnet-4-6",
    content, stop_reason: stop, stop_sequence: null,
    usage: { input_tokens: 12, output_tokens: 12 },
  });
}

function pickArgs(convo) {
  const c = convo.toLowerCase();
  const isGH = c.includes("repo") || c.includes("github") || c.includes("pull request") ||
    c.includes("issue") || c.includes(" pr ") || c.includes("gist");
  if (isGH) {
    // gh api (REST) so it works against the REST-only emulator shim.
    if (c.includes("create")) return ["api", "-X", "POST", "/user/repos", "-f", "name=ask-demo-repo"];
    return ["api", "/user/repos"];
  }
  if (c.includes("create")) return ["s3api", "create-bucket", "--bucket", "ask-demo-bucket"];
  return ["s3api", "list-buckets"];
}

http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    let j = {};
    try { j = JSON.parse(body); } catch (_) {}
    const convo = JSON.stringify(j.messages || []);
    res.setHeader("content-type", "application/json");
    if (convo.includes("tool_result")) {
      res.end(message(j.model, [{ type: "text", text: "Done — see the command output above (summarized by mock)." }], "end_turn"));
      return;
    }
    res.end(message(j.model, [
      { type: "text", text: "I'll run a command to answer that." },
      { type: "tool_use", id: "toolu_mock_1", name: "run_command", input: { args: pickArgs(convo) } },
    ], "tool_use"));
  });
}).listen(PORT, "127.0.0.1", () => console.log(`anthropic-mock on http://127.0.0.1:${PORT}`));
