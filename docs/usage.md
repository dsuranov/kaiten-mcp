# Usage

## CLI output contract

Every successful API command writes one indented JSON value and one trailing
newline to stdout. Logs and progress messages use stderr. Ordinary failures
return status `1`; interruption may return `130`.

Help, completion, and version output do not require credentials:

```sh
kaiten --help
kaiten boards --help
kaiten cards update --help
kaiten completion bash
kaiten --version
```

## Read examples

```sh
kaiten spaces list
kaiten spaces get 42
kaiten boards list --space-id 42
kaiten boards get 84
kaiten columns list --board-id 84
kaiten lanes list --board-id 84
kaiten cards list --board-id 84
kaiten cards list --board-id 84 --archived
kaiten cards get 126
kaiten comments list --card-id 126
kaiten members list --card-id 126
kaiten tags list
kaiten tags card-tags --card-id 126
kaiten checklists get --card-id 126 --checklist-id 7
```

`cards list` returns active cards by default. `--archived` selects archived
cards and `--all` selects both; the two flags cannot be combined.

Because stdout is only JSON, output can be piped directly to a processor:

```sh
kaiten cards list --board-id 84 | jq 'map({id, title})'
```

## Card mutations

Create with an ID selector:

```sh
kaiten cards create \
  --title "Review draft" \
  --board-id 84 \
  --column-id 5 \
  --description "Review the current draft" \
  --due-date "2026-08-15T12:00:00Z"
```

Or select a column and optional lane by name:

```sh
kaiten cards create \
  --title "Prepare notes" \
  --board-id 84 \
  --column-name "Ready" \
  --lane-name "General"
```

Name comparison is case-insensitive. Exact match wins, otherwise only a unique
substring is accepted. Supplying both a name and ID for the same selector is an
error, and an ambiguous match cannot mutate data.

Updates send only flags that were explicitly supplied:

```sh
kaiten cards update 126 --title "Review final draft" --size 3
kaiten cards archive 126
kaiten cards unarchive 126
kaiten cards delete 126
```

Permanent deletion is sent once. A timeout after submission is reported as an
uncertain outcome and does not cause an automatic retry.

## Other mutations

```sh
kaiten comments add --card-id 126 --text "Ready for review"
kaiten blockers block --card-id 126 --reason "Waiting for approval"
kaiten blockers unblock --card-id 126 --blocker-id 9
kaiten tags add --card-id 126 --name "priority"
kaiten tags remove --card-id 126 --tag-id 4
kaiten checklists create --card-id 126 --name "Release"
kaiten checklists add-item --card-id 126 --checklist-id 7 --text "Run tests"
kaiten checklists check --card-id 126 --checklist-id 7 --item-id 8
kaiten checklists uncheck --card-id 126 --checklist-id 7 --item-id 8
kaiten checklists delete --card-id 126 --checklist-id 7
```

When `--reason` is omitted from `blockers block`, the CLI sends
`Blocked via Kaiten CLI`. This keeps the optional CLI flag compatible with the
current public API, which requires a nonempty blocker reason.

Every positive numeric ID, required text value, enumeration, and date is
validated before mutation. Empty required text is rejected.

## MCP stdio

With `KAITEN_URL` and `KAITEN_API_TOKEN` inherited by the process, a generic MCP
client can launch:

```json
{
  "mcpServers": {
    "kaiten": {
      "command": "kaiten-mcp",
      "args": []
    }
  }
}
```

In stdio mode, stdout is reserved for MCP protocol messages. Configure client
diagnostics to capture stderr separately.

## MCP Streamable HTTP

```sh
kaiten-mcp \
  --transport streamable-http \
  --host 127.0.0.1 \
  --port 8000 \
  --streamable-http-path /mcp
```

Check server readiness without invoking a tool:

```sh
curl --fail --silent http://127.0.0.1:8000/health | jq .
```

The health object contains `status`, `version`, and `runtime`. Use a real MCP
client, rather than a handcrafted HTTP request, for initialization, protocol
negotiation, `tools/list`, and tool calls.

## MCP tools

Read-only startup publishes these 18 tools:

```text
get_board                 get_board_structure       get_card
get_card_checklists       get_card_children         get_current_user
get_member_cards          get_my_cards              get_responsible_cards
get_server_info           get_space                 list_boards
list_card_types           list_custom_properties    list_spaces
list_tags                 list_users                search_cards
```

Set `KAITEN_ENABLE_WRITE_TOOLS=true` before startup to additionally publish:

```text
add_checklist_item        add_comment               add_external_link
add_watcher               create_card               create_checklist
delete_checklist          delete_checklist_item     link_child_card
move_card                 remove_member             set_responsible
unlink_child_card         update_card               update_checklist_item
```

Enabling registration does not perform a write. A write occurs only after a
client explicitly calls a write tool.

## MCP results

Successful domain calls return the same object in `structuredContent` and as
indented JSON text:

```json
{
  "ok": true,
  "data": {
    "id": 42,
    "title": "Example"
  },
  "meta": {
    "source": "kaiten",
    "cached": false
  }
}
```

Domain failures use `auth`, `not_found`, `rate_limit`, `validation`, or
`upstream`:

```json
{
  "ok": false,
  "error": {
    "type": "not_found",
    "message": "the requested resource was not found",
    "status_code": 404
  }
}
```

Schema violations and malformed MCP requests are protocol-level failures, not
domain envelopes.
