# winky

winky is a terminal coding agent. You describe a task in natural language, and the model can read and edit workspace files, run shell commands, and search or fetch web pages. The TUI is built with Bubble Tea.

The project is early-stage and focuses on the agent loop, tool permissions, and session persistence.

## Features

- Streaming model output, reasoning, and tool calls
- File read, write, edit, delete, and directory operations
- Shell commands, web search, and web fetch
- Permission levels with per-action approval
- JSONL session transcripts stored locally
- Request history projected from the transcript
- Automatic context compaction with a summarized handover
- Model catalog and OpenAI Responses-compatible client
- Optional system prompt and token/context usage display

## Requirements

- Go 1.27 or newer

## Install

```bash
git clone <repo-url>
cd winky
go mod download
```

Create the local configuration and API key files:

```bash
cp config.example.json config.json
cp .env.example .env
```

Set the API key in `.env`:

```dotenv
API_KEY=sk-your-key-here
```

Keep both files local. `.env` is git-ignored; do not commit real credentials.

## Configuration

Minimal configuration:

```json
{
  "provider": {
    "name": "deepseek",
    "model": "deepseek-flash",
    "maxOutputTokens": 16384,
    "contextWindow": 1000000
  },
  "agent": {
    "temperature": 0.7
  }
}
```

| Field | Description |
| --- | --- |
| `provider.name` | Provider name, such as `deepseek` or `openai`; usually matches the provider in `catalog.json` |
| `provider.model` | Model name; must exist in `catalog.json` |
| `provider.baseUrl` | Optional override for the API base URL from `catalog.json` |
| `provider.maxOutputTokens` | Maximum output tokens per request |
| `provider.contextWindow` | Optional context window override, used for the TUI usage display |
| `agent.temperature` | Sampling temperature |
| `agent.compaction` | Automatic context compaction settings; see [Context compaction](#context-compaction) |
| `agent.agentDir` | Optional session directory root; defaults to `~/.winky` |
| `cwd` | Optional workspace root and `.env` lookup directory; defaults to the current directory |

`catalog.json` maps model names to providers and protocols. Only the `openai-responses` protocol is currently implemented.

## Run

```bash
go run ./cmd/tui -config config.json
```

Or build first:

```bash
go build -o winky ./cmd/tui
./winky -config config.json
```

Command-line flags:

| Flag | Default | Description |
| --- | --- | --- |
| `-config` | empty | Path to the config file. Without it, defaults are used and `provider.model` still needs to be set |
| `-catalog` | `catalog.json` | Path to the model catalog |
| `-prompt` | empty | Optional system prompt file |

When running from another directory, pass `-catalog` with the correct path.

## Usage

Type a task and press Enter. The model can call these tools:

- `read_file`
- `write_file`
- `edit_file`
- `list_dir`
- `delete`
- `mkdir`
- `shell`
- `web_search`
- `web_fetch`

The default permission is `workspace`: reads and writes inside the workspace are allowed automatically. Access outside the workspace, shell commands, and network requests require approval or a higher permission level.

Available commands:

| Command | Effect |
| --- | --- |
| `/readonly` | Read-only mode |
| `/workspace` or `/write` | Workspace read/write mode |
| `/full` | Full access, including outside paths, shell, and network |
| `/compact` | Summarize old turns now, without waiting for the threshold |

When an approval prompt appears:

- `y` allows the action
- `n` or `Esc` denies it

Key bindings:

| Key | Action |
| --- | --- |
| `Enter` | Send |
| `Ctrl+J` | Insert a newline |
| `Esc` | Clear a draft if present; otherwise cancel the current turn |
| `Ctrl+C` | Quit |
| `PgUp` / `PgDn` | Scroll |
| `Ctrl+T` | Expand or collapse all reasoning |

## Session data

Each launch creates a new session:

```text
~/.winky/sessions/<timestamp>-<random>/
  meta.json
  transcript.jsonl
```

- `meta.json` stores the session id, creation time, working directory, and model information.
- `transcript.jsonl` is an append-only event stream containing user messages, assistant output, reasoning, tool results, and turn/step boundaries.

Each run starts a new session. `--resume` is not implemented yet.

## Context compaction

A long conversation eventually outgrows the model's context window. When the
projected request reaches `agent.compaction.triggerPercent` of the usable window
(the window minus `maxOutputTokens`), winky summarizes the oldest turns and
appends a `compaction_summary` event to the transcript. The recent turns are
kept verbatim.

- The summary is sent as a system message, so `instructions` (the system
  prompt) stays the stable prefix that provider-side caching relies on.
- The transcript stays append-only: compaction changes what is *sent* to the
  model, not what is stored on disk.
- Compaction is bounded per turn: at most one check at the turn boundary and
  one between steps, so it cannot loop on its own model calls.
- Automatic compaction needs a known window; set `provider.contextWindow` or
  add `contextWindow` to the model's `catalog.json` entry. Otherwise it is
  skipped.
- `/compact` runs the same compaction on demand. It ignores the thresholds and
  the `enabled` switch, keeps roughly a quarter of the current context, and
  reports "nothing to compact" when the history already fits.

```json
{
  "agent": {
    "compaction": {
      "enabled": true,
      "triggerPercent": 80,
      "hardPercent": 92,
      "keepPercent": 30,
      "maxPromptTokens": 0,
      "maxSummaryTokens": 1024,
      "model": ""
    }
  }
}
```

| Field | Description |
| --- | --- |
| `enabled` | Turn automatic compaction on |
| `triggerPercent` | Threshold as a percentage of the usable window, checked before a turn starts |
| `hardPercent` | Higher threshold checked between the steps of one turn |
| `keepPercent` | Share of the trigger threshold kept verbatim, measured from the newest turn back |
| `maxPromptTokens` | Optional absolute cap on the trigger threshold; `0` disables the cap |
| `maxSummaryTokens` | Cap on the generated summary |
| `model` | Model used for the summary; empty reuses `provider.model` |

## Project layout

```text
cmd/tui/                    entry point and agent wiring
internal/agent/             agent loop, history projection, and context compaction
internal/llm/               request/response types and OpenAI Responses client
internal/llm/api/           provider adapters
internal/tool/              filesystem, shell, web tools and permission policy
internal/tui/               Bubble Tea UI
internal/schema/            shared UI events and Turn types
internal/session/           session directory and meta.json
internal/transcript/        transcript events and codec
internal/transcript/jsonl/  JSONL recorder
```

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

## Current limitations

- Only the `openai-responses` protocol is supported; other protocols in `catalog.json` are not wired up.
- Only new sessions are supported, with no resume.
- Compaction keeps the full history on disk: it changes the request, not `transcript.jsonl`.
- Compaction only cuts at turn or step boundaries, so a request that is still too large after compacting fails with an error instead of dropping context silently.
- The transcript writer assumes a single process; there is no file locking.
- The project is early-stage, so APIs and behavior may change.
