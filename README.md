# outlookcache

`outlookcache` reads the mail that new Outlook for Mac keeps on your computer. It prints accounts, folders, messages and message bodies as JSON, and it can also run as a read-only MCP server for AI assistants.

It does not use Microsoft Graph, EWS, IMAP, OAuth or the network. It reads only what the signed-in Outlook client already stores on your disk.

## Why

To read mail through Microsoft Graph, you need an app registration and permissions such as `Mail.Read`, and many organizations allow them only after admin consent. New Outlook for Mac already keeps recent mail in a local cache file, `HxStore.hxd`. `outlookcache` reads it from there.

## Install

With Homebrew on macOS:

```bash
brew install --cask stn1slv/tap/outlookcache
```

Binaries for macOS (Apple silicon and Intel) are on the [releases page](https://github.com/stn1slv/outlookcache/releases). The binaries are not signed. If macOS blocks a downloaded binary, run `xattr -d com.apple.quarantine outlookcache`.

With Go 1.27 or later:

```bash
go install github.com/stn1slv/outlookcache/cmd/outlookcache@latest
```

Or build from source with `make build`; the binary is written to `bin/outlookcache`.

You need new Outlook for Mac that has been signed in at least once. Legacy Outlook for Mac (`Outlook.sqlite`) is not supported. If macOS denies access to the store, give your terminal app (or the app that starts the MCP server) Full Disk Access in System Settings, Privacy & Security.

## Usage

### Commands

```bash
outlookcache accounts                         # mail accounts and the IDs of their standard folders
outlookcache folders                          # folders with total, unread and unconfirmed counts
outlookcache messages --folder inbox --unread # unread messages in the Inbox, newest first
outlookcache messages --days 7                # messages of the last 7 days
outlookcache search "quarterly budget"        # text in subject, preview, sender, recipients or attachment names
outlookcache search "budget" --body           # also search the body text (slower)
outlookcache read 18800001                    # one message with its body as plain text
outlookcache thread 18800101                  # all messages of a conversation, oldest first
```

`messages` and `search` print messages without bodies. Use the `id` of a row with `read`, and its `conversation_id` with `thread`. By default `read` and `thread` cut off quoted and forwarded text, so each message shows only its new text; `--quoted` keeps it.

All commands print JSON to stdout and a summary to stderr, for example `# 12 result(s), 3 record(s) skipped, 0 damaged section(s), 123 undecoded block(s), 2 unconfirmed`. The counts are explained in [Honest output](#honest-output).

### Flags and defaults

| Command | Flags and defaults |
|---------|--------------------|
| `messages` | `--folder`, `--unread`, `--days 0`, `--limit 50`, `--confirmed-only` |
| `search QUERY` | the same as `messages`, plus `--body` |
| `read MESSAGE_ID` | `--quoted` |
| `thread CONVERSATION_ID` | `--quoted`, `--limit 20` (the newest messages are kept) |
| `mcp` | `--cache-ttl 3m` (a Go duration, such as `180s` or `3m`) |

`--limit 0` means no limit. `--days 0` means all dates. `--folder` takes a folder ID, a role (`inbox`, `sent`, `drafts`, `deleted`, `junk`, `archive`, `outbox`) or a folder name, in this order.

Global flags:

| Flag | Meaning |
|------|---------|
| `--account ID_OR_ADDRESS` | Only one account, by ID or e-mail address (see `outlookcache accounts`). With `mcp`, every tool serves only this account. |
| `--store PATH` | Read this `HxStore.hxd` instead of the one of the default profile. |
| `--max-content N` | Characters of body text in `read`, `thread` and the MCP results (`0` means no limit, default `4000`). |
| `-v`, `--verbose` | Log load statistics to stderr. |

Environment variables:

| Variable | Meaning |
|----------|---------|
| `OUTLOOKCACHE_STORE` | Default for `--store`. |
| `OUTLOOKCACHE_MAX_CONTENT` | Default for `--max-content`. |
| `OUTLOOKCACHE_CACHE_TTL` | Whole number of seconds the MCP server reuses a decoded store (default `180`). |

The default store is `~/Library/Group Containers/UBF8T346G9.Office/Outlook/Outlook 15 Profiles/Main Profile/HxStore.hxd`.

## MCP server

`outlookcache mcp` runs a read-only MCP server on stdin and stdout. Example configuration for Claude Desktop or Claude Code:

```json
{
  "mcpServers": {
    "outlook": { "command": "outlookcache", "args": ["mcp"] }
  }
}
```

Add `"--account", "you@example.com"` to the arguments to serve only one account, or `"--max-content", "0"` for full body text.

| Tool | Purpose | Key arguments and defaults |
|------|---------|----------------------------|
| `list_accounts` | Mail accounts and the IDs of their standard folders. | `account` |
| `list_folders` | Folders with total, unread and unconfirmed counts. | `account` |
| `list_messages` | Messages without bodies, newest first. | `folder`, `unread_only` (`false`), `days` (`0`), `limit` (`50`), `confirmed_only` (`false`), `account` |
| `search_messages` | Text search in subject, preview, sender, recipients and attachment names. | `query` (required), `in_body` (`false`), and the arguments of `list_messages` |
| `read_message` | One message with its body as plain text. | `id` (required, the `id` field of a row), `include_quoted` (`false`), `account` |
| `read_thread` | The messages of a conversation with their bodies, oldest first. | `conversation_id` (required), `include_quoted` (`false`), `limit` (`20`), `account` |

The server decodes the store on the first tool call and keeps it in memory. It decodes the store again when the TTL ends or when Outlook changes the file.

Every tool response keeps its rows in `result`. It also has `more` (the limit cut off more matches), the counts described in [Honest output](#honest-output), `partial` and `content_trust`.

Mail is written by other people, so an AI assistant must not follow instructions found in it. Every tool description, the server instructions and the `content_trust` field of every response say so.

## Honest output

`outlookcache` reads an undocumented file format, so it reports what it could not read instead of hiding it:

| Field | Meaning |
|-------|---------|
| `confirmed` | `true` when the block index of the store confirms the message. Messages with `false` may already be deleted, because the reader cannot yet tell all deleted messages from live ones. `--confirmed-only` (MCP: `confirmed_only`) hides them. |
| `skipped_records` | Messages that could not be decoded. |
| `damaged_sections` | Blocks in the index that fail their checksum. |
| `unindexed_damaged` | Failing blocks without an index entry. Most are old copies. |
| `undecoded_blocks` | Blocks in a format the reader does not know yet. They may hold recent changes. |
| `unreadable_bodies` | Bodies that could not be read, for example a missing EFMData file. |
| `partial` | MCP only: `true` when any of the counts above is not zero, so a result may be incomplete. |

On a real store, `undecoded_blocks` is usually not zero, so `partial` is usually `true`. An empty result then does not prove that no matching message exists.

The body of a message has a `kind`: `inline` (stored in the store), `efmdata` (stored in a separate file below `Files/S0/<n>/EFMData`), `none` (only the preview is stored, as for most meeting items) or `unparsed` (a layout the reader does not know).

## How it works

New Outlook for Mac keeps its mail cache in `HxStore.hxd`, the "Hx" store also used by the Mail app of Windows. The file is a sequence of 4 KB pages with LZ4-compressed blocks. `outlookcache`:

1. copies `HxStore.hxd` to a private temporary folder, reads the copy into memory and removes the copy,
2. checks every block header and payload against its CRC, and decodes the LZ4 blocks with its own decoder,
3. finds the objects in the blocks: accounts, folders, messages, recipients and attachments,
4. keeps the current copy of each message, because Outlook writes a new copy on every change and leaves the old ones in the file,
5. reads bodies on request, from the message block or from an EFMData file (read only, in place), and converts the HTML to plain text.

The container format follows the public [HxStore specification](https://github.com/securized/hxstore-reverse-engineering) by securized. The record layout (folders, accounts, read state, recipients, attachments and bodies) was mapped for this project with Outlook 16.115. See [docs/STORAGE.md](docs/STORAGE.md) for the format, the confidence of each field and the open questions, and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the license.

A new Outlook version can change the layout. When that happens, the counts in the summary go up; please open an issue with the Outlook version and the summary line (never with mail content).

## Privacy

The tool is read-only and makes no network calls. It never changes, locks, moves or deletes Outlook files. The temporary copy is created with owner-only permissions and removed after each load. Output can contain mail of every account in the store, so treat it as confidential.

## Development

```bash
make setup        # download dependencies
make test         # unit tests with the race detector
make lint         # golangci-lint and gofumpt
make format       # format the code
make build        # build bin/outlookcache
make vulncheck    # check for known vulnerabilities
make upgrade-deps # update dependencies and pinned Go tools
```

Tests use synthetic data only. `internal/hxtest` writes a synthetic store with two accounts, folders, messages, inline and EFMData bodies. `cmd/hxdiscover` is a development tool for mapping the format; it prints only counts, offsets and redacted shapes, and it is not part of the release.

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
