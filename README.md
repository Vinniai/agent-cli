# ask

`ask` is a natural-language frontend for command-line tools. Describe what you
want in plain English; `ask` translates it into the correct underlying CLI
command, runs it (read-only commands run freely; mutating commands ask for
confirmation), and answers.

It ships with providers for the **AWS CLI** (`aws`) and the **GitHub CLI**
(`gh`), and a small `Provider` interface for adding more. The natural-language
translation is powered by Claude through the Anthropic API, so you need a model
API key (see [Configuration](#configuration)). `ask` also keeps direct,
scriptable access to the Anthropic API itself (the `messages`, `models`, and
`beta:*` commands).

## Install

Requires [Go](https://go.dev/doc/install) 1.22+, plus the CLIs you want to drive
(`aws`, `gh`).

```sh
git clone https://github.com/Vinniai/agent-cli.git
cd agent-cli
make build          # produces ./ask  (equivalently: go build -o ask ./cmd/ant)
```

Run it without building during development:

```sh
./scripts/run args...
```

## Quickstart

```sh
export ANTHROPIC_API_KEY=sk-ant-...      # the model that does the translation

# AWS
./ask aws "list my S3 buckets in us-east-1"
./ask aws --all-profiles "how many running EC2 instances per account?"
./ask aws "list buckets in prod"          # target account inferred from the question

# GitHub (gh must be authenticated: gh auth status)
./ask gh "list my open pull requests"
```

Run a provider with no prompt to drop into an interactive REPL: `./ask aws`.

**Quote your prompt** when it contains characters your shell treats specially
(`?`, `'`, `|`, `*`). Plain words work unquoted (`ask aws list my buckets`), but
`ask aws how many buckets?` makes the shell try to glob `buckets?` — so quoting
is the safe habit.

### Built-in help

Every entry point prints usage with copy-pasteable examples:

```sh
ask                 # or: ask help | ask --help | ask -h  — overview + examples
ask aws --help      # provider flags (--model --profile --all-profiles --yes/-y)
ask auth --help     # authentication commands
```

See [TESTING.md](./TESTING.md) for the full test/usage guide, including a local
emulator sandbox you can exercise without real cloud credentials.

## How it works

- A small **Provider** (`pkg/cmd/provider_*.go`) describes each CLI: its binary,
  a read/write classifier (so writes can be gated), how to enumerate accounts,
  and how to target one.
- The agent loop asks the model for a command, executes it, feeds the output
  back, and repeats until it can answer.
- **Safety:** read commands run immediately; write/unknown commands print the
  exact command and wait for `y/N` (override with `--yes`). The executed command
  and its output go to stderr; the final answer goes to stdout (so it stays
  pipeable).
- **Account selection:** `--profile <name>` pins one account, `--all-profiles`
  fans out across all of them, or—with neither flag—the target is inferred from
  your wording (e.g. "buckets in prod").

### Adding a provider

Implement the `Provider` interface in a new `pkg/cmd/provider_<x>.go` and add one
line to the `Commands` list in `pkg/cmd/cmd.go`. Use `provider_aws.go` /
`provider_gh.go` as templates.

## Configuration

`ask` needs credentials for the model (the brain) and for each CLI it drives.

**Model (the brain)** — authenticate the model in one of two ways. Choose the
model per call with `--model` (default `claude-sonnet-4-6`).

1. **OAuth (log in with your Claude account)** — no API key needed:

   ```sh
   ./ask auth login          # opens a browser; grants the user:inference scope
   ./ask aws "list my S3 buckets"
   ```

   The token is stored as a profile, auto-refreshed, and used by every command
   (including `aws`/`gh`). Manage logins with `./ask auth status` /
   `./ask auth logout`; select a named login with the global `--profile` flag.

   Already logged into **Claude Code** on this machine (macOS)? Reuse that
   keychain login instead of a separate `ask auth login`:

   ```sh
   make claude-auth        # syncs the Claude Code OAuth token into an ask profile
   ./ask aws "list my S3 buckets"
   ```

   Re-run `make claude-auth` if the token expires. Note: inference goes against
   your Claude subscription, so its rate limits apply (shared with Claude Code).

2. **API key / auth token** — set one of these (or the matching flag):

   | Environment variable   | Notes            |
   | ---------------------- | ---------------- |
   | `ANTHROPIC_API_KEY`    | standard API key |
   | `ANTHROPIC_AUTH_TOKEN` | bearer token     |

> Precedence: `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` (env or flag) win over
> a logged-in profile. To use OAuth, make sure those aren't set (`unset
> ANTHROPIC_API_KEY`). `./ask auth status` shows which credential is active.

**CLI tools** — configure each as normal: `aws configure` / `aws sso login`, and
`gh auth login`.

## Direct API access

`ask` retains the full Anthropic API command surface for scripting:

```sh
./ask messages create \
  --max-tokens 1024 \
  --message '{content: [{text: x, type: text}], role: user}' \
  --model claude-sonnet-4-6
```

For details about any command, use `--help`.

### Global flags

- `--api-key` (can also be set with `ANTHROPIC_API_KEY`)
- `--auth-token` (can also be set with `ANTHROPIC_AUTH_TOKEN`)
- `--webhook-key` (can also be set with `ANTHROPIC_WEBHOOK_SIGNING_KEY`)
- `--help` — show command line usage
- `--debug` — enable debug logging (HTTP request/response details)
- `--version`, `-v` — show the CLI version
- `--base-url` — use a custom API backend URL (selects which model endpoint)
- `--format` — output format (`auto`, `explore`, `json`, `jsonl`, `pretty`, `raw`, `yaml`)
- `--format-error` — output format for errors
- `--transform` / `--transform-error` — transform output using [GJSON syntax](https://github.com/tidwall/gjson/blob/master/SYNTAX.md)

### Passing files as arguments

Pass files to API commands with the `@myfile.ext` syntax:

```bash
./ask <command> --arg @abe.jpg
./ask <command> --arg '{image: "@abe.jpg"}'
```

Escape a literal leading `@` with `\@`. For explicit encoding use
`@file://myfile.txt` (string) or `@data://myfile.dat` (base64).

## Development

- **Tests:** `make test` (provider unit tests, no servers) or `make test-all`
  (full suite; starts the API mock server first). See [TESTING.md](./TESTING.md).
- **Link a different Anthropic Go SDK version:** `./scripts/link github.com/org/repo@version`
  (or a local path; defaults to `../anthropic-go`).
