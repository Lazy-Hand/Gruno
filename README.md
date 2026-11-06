# Gruno

Bruno-style API client workbench built on [mygo](https://github.com/egoist/mygo)'s native UI — no webview, no JavaScript frontend.

## What it does

- Opens **Bruno collections** directly: `bruno.json`, `collection.bru`, `folder.bru`, `environments/*.bru`, and one `.bru` file per request. Folders are directories on disk; everything is Git-diffable.
- Edits write the `.bru` file **live** — the file on disk is the truth, like Bruno itself.
- Sends requests over Go's `net/http` with `{{var}}` resolution in Bruno's precedence order: collection vars → folder vars (outer→inner) → active environment → request `vars:pre-request`.
- Collection/folder headers apply to every request unless a request header of the same name overrides them.
- Auth: none / inherit / basic / bearer / apikey / digest (two-round RFC 7616 MD5). OAuth2/AWSv4/WSSE/NTLM parse and keep their block data but are not yet executed.
- Bodies: none, json, text, xml, form-urlencoded, multipart-form, graphql (sent as `{query, variables}` JSON).
- Scripts/tests/docs/assert blocks round-trip through the editor; pre-request/post-response scripts are kept but not executed (no JS runtime in this iteration).

## Layout

```
bru/          .bru parser + serializer (dictionary, text, array blocks)
store.go      collection directory: scan, create, save, rename, move, delete
send.go       bru.Request → SendInput adaptation (auth, body, scopes, headers)
request.go    variable resolution + *http.Request construction
http_service.go  send + response classification (30s timeout, 10MB body cap)
digest.go     HTTP Digest auth two-round exchange
app.go        native UI: sidebar tree, tabs, request editor, response pane (Bruno layout: sections Params/Body/Headers/Auth/Vars/Script/Tests/Assert/Docs)
main.go       window + single-instance + app menu
```

## Run

```sh
bun install
bun run dev     # mygo dev (native window)
bun run build   # packaged .app + .dmg in build/
```

On first launch pick a Bruno collection directory (or create one); the
choice is remembered in `PathUserData/gruno.json`.

`GRUNO_AUTOPEN=1` opens the first request on launch (used for screenshots/UI automation).

## Tests

```sh
go test . ./bru
```

- `bru/` — parser/serializer round-trips, dict/text/array blocks, disabled `~` rows.
- `store_test.go` — collection lifecycle on temp dirs: create/open, CRUD, nested folder vars, env persistence, send adaptation, inherited auth.
- `request_test.go`, `http_service_test.go` — variable resolution, request building, response classification against `httptest` servers.
- `app_test.go` — `ui.NewTester` smoke tests: welcome screen, tree selection, section tabs, send round-trip.

## Not yet (scope of this iteration)

- JS script execution (scripts/tests parse and edit; a JS engine like goja is the next step).
- OAuth2/AWSv4/WSSE/NTLM request signing.
- Collection runner, codegen, Postman/Insomnia import.
