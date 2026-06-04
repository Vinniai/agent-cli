# Testing `ask`

`ask` is a natural-language frontend over real CLIs. It has **two independent
connections**, and most "it didn't work" confusion comes from configuring one
but not the other:

| Layer | What it is | Selected by |
|---|---|---|
| **Brain** | the model that turns your sentence into a command | `--base-url` (which endpoint) + `ANTHROPIC_API_KEY` / `ask auth login` |
| **Tool** | the real CLI `ask` shells out to (`aws`, `gh`, …) | that CLI's own env/config (endpoint + credentials) |

Mnemonic: **`--base-url` = which Claude; `AWS_ENDPOINT_URL` / `GH_HOST` = which cloud.**

Prereqs: Go (`go`), and the CLIs you want to drive (`aws`, `gh`). For the
emulator flow you also need Node and the local `agent-emulate` checkout.

---

## 1. Unit tests — the real proof (no servers, no network)

```bash
make test
# or: go test ./pkg/cmd/ -run 'AWS|GH|Provider|AgentLoop' -v
```

Covers classification (read/write/unknown), profile/host enumeration, the agent
loop, the write-confirmation gate, the transparency echo, multi-account fan-out,
**account inference**, and **explicit-flag override**.

Full suite (includes the generated API tests, which need the Stainless mock on
`:4010`): `make test-all`.

---

## 2. Build & explore

```bash
make build           # -> ./ask
./ask --help         # 'ask'; aws + gh under ASSISTANT
./ask aws --help     # flags: --model --profile --all-profiles --yes
```

---

## 3. Real usage (real Claude + real cloud)

This is what you ship. **No `--base-url`, no emulator env.**

```bash
export ANTHROPIC_API_KEY=sk-ant-...     # or: ./ask auth login

# AWS — configure real creds first
aws configure                            # or: aws sso login --profile prod
./ask aws "list my S3 buckets in us-east-1"
./ask aws --profile prod "how many running EC2 instances?"
./ask aws --all-profiles "total S3 bucket count across all accounts"
./ask aws "list buckets in prod"         # account INFERRED from the sentence

# GitHub — gh must be authenticated (gh auth status)
./ask gh "list my open pull requests"
./ask gh "show issues assigned to me in the acme org"
```

### Account selection (three ways)
1. **Pin one:** `--profile prod` (authoritative; the model can't override it).
2. **All accounts:** `--all-profiles` (fans out across every profile in `~/.aws/config`).
3. **Inferred from the question:** with neither flag, `ask` advertises your
   available profiles to the model, which picks the matching one — e.g.
   "buckets in **prod**" → runs with `--profile prod`; "in **every** account" →
   fans out. (Inference reads `~/.aws/config` profiles / `gh` `hosts.yml` hosts.)

### Safety
Read commands run immediately. Write/unknown commands print the exact command
and wait for `y/N` (default No). `--yes` bypasses the gate — be deliberate with
it against real infrastructure. The executed command + its output go to
**stderr**; the model's summary goes to **stdout** (so stdout stays pipeable).

---

## 4. Emulator flow (safe sandbox — no real cloud, no real key)

Uses a scripted brain mock (`testing/anthropic-mock.js`) + `agent-emulate`.
The mock is NOT intelligent — it returns fixed commands by keyword; it only
exercises the plumbing. Real translation needs section 3.

```bash
make servers-up                          # starts :4001 github, :4006 aws, :4009 brain-mock
make demo-aws PROMPT="list my buckets"
make demo-aws PROMPT="create a bucket"   # write -> runs (emulator)
make demo-gh  PROMPT="list my repos"     # uses the gh-api shim -> emulator
make repl-aws                            # interactive
make servers-down                        # stops :4001 and :4009 (leaves :4006)
```

Manual equivalents (what the Makefile runs):

```bash
# AWS: route the real aws CLI at the emulator + dummy creds
export ANTHROPIC_API_KEY=dummy
export AWS_ENDPOINT_URL=http://localhost:4006 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1
./ask --base-url http://localhost:4009 aws --yes "list my buckets"

# GitHub: the emulator is REST-only, so use the gh-api forwarding shim on PATH
export ANTHROPIC_API_KEY=dummy GH_ENTERPRISE_TOKEN=test_token_admin
PATH="$PWD/testing/gh-shim:$PATH" ./ask --base-url http://localhost:4009 gh --yes "list my repos"
```

### Real `gh` binary ↔ emulator (optional, needs admin once)
The real `gh` forces HTTPS and (on macOS) ignores `SSL_CERT_FILE`, so it needs a
TLS proxy whose cert is trusted in the keychain. `gh repo list` uses GraphQL,
which the emulator does NOT implement — only `gh api` (REST) works.

```bash
# self-signed cert + a proxy that strips /api/v3 and forwards to :4001
mkdir -p /tmp/ghtls && cd /tmp/ghtls
openssl req -x509 -newkey rsa:2048 -nodes -keyout key.pem -out ca.pem -days 2 \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost"
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/ghtls/ca.pem
# (run the proxy: node -e '...' terminating TLS on :4011 -> http://localhost:4001)
GH_HOST=localhost:4011 GH_ENTERPRISE_TOKEN=test_token_admin \
  ANTHROPIC_API_KEY=dummy ./ask --base-url http://localhost:4009 gh "list issues"
# undo: sudo security delete-certificate -c localhost /Library/Keychains/System.keychain
```

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `NoCredentials … aws login` | `aws` hit real AWS with no creds | set `AWS_ENDPOINT_URL` + dummy creds (emulator), or `aws configure` (real) |
| every prompt runs the same command | you're on the scripted mock | drop `--base-url …:4009`, use a real `ANTHROPIC_API_KEY` |
| `connection refused :4006/:4001` | emulator not running | `make servers-up` |
| gh: `certificate signed by unknown authority` | real gh doesn't trust the proxy cert | trust it in the keychain (section 4) or use the shim |
| write ran without asking | `--yes` was set | drop `--yes` to get the confirmation gate |
