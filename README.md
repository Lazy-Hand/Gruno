# Gruno

Bruno-style API client workbench built on [mygo](https://github.com/egoist/mygo)'s native UI — no webview, no JavaScript frontend.

## What it does

- Opens **Bruno collections** directly: `bruno.json`, `collection.bru`, `folder.bru`, `environments/*.bru`, and one `.bru` file per request. Folders are directories on disk; everything is Git-diffable.
- Edits write the `.bru` file **live** — the file on disk is the truth, like Bruno itself. Editor saves are debounced (300 ms after the last keystroke) and flushed on tab close, send and ⌘S; the file's CRLF line endings are kept.
- Sends requests over Go's `net/http` with `{{var}}` resolution, lowest precedence first: `{{process.env.X}}` (collection `.env`, then the OS) → collection vars → folder vars (outer→inner) → active environment → request `vars:pre-request` → runtime vars (`bru.setVar`).
- Environment secrets: a variable marked **Secret** in the environment editor is listed in the file's `vars:secret` block by name only; its value is sealed with AES-256-GCM in `PathUserData/env-secrets.json`, under a key kept in the macOS Keychain. Secrets resolve like other environment variables, and `bru.setEnvVar` on a secret updates the stored value (`{persist: true}`).
- Network settings at Bruno's three levels: preferences (⌘,) for TLS verification, an extra CA certificate and the default timeout; the collection's proxy (system / none / custom http, https or socks5 with auth and a bypass list) and client certificates (PEM or PFX, matched by domain) in `bruno.json`; the request's `settings` block for timeout and redirects.
- `bruno.json` is edited in place — keys Gruno does not use, and their order, are kept; `.bru` blocks it does not model are written back verbatim.
- Collection/folder headers apply to every request unless a request header of the same name overrides them; folders have their own settings dialog (headers, auth, vars, scripts, tests, docs) writing `folder.bru`.

- `meta.tags` arrays are preserved, so tag-filtered Bruno runs keep working.
- Dynamic variables resolve as Bruno's do: `{{$guid}}`, `{{$timestamp}}`, `{{$isoTimestamp}}`, `{{$randomInt}}`, `{{$randomBoolean}}`, `{{$randomFirstName}}`, `{{$randomLastName}}`, `{{$randomFullName}}`, `{{$randomEmail}}`, `{{$randomUserName}}`, `{{$randomCity}}`, `{{$randomCountry}}`, `{{$randomPhone}}`, `{{$randomIP}}`, `{{$randomPassword}}`, `{{$randomLorem}}`, `{{$randomDate}}`.
- Auth: none / inherit / basic / bearer / apikey / digest (two-round RFC 7616 MD5, credentials resolved from variables) / OAuth2 (`client_credentials`, `password` and `authorization_code` with PKCE S256 — the browser opens, a loopback listener takes the redirect, and the code is exchanged; credentials in the body or a Basic header; token in the `Authorization` header with a prefix or in the URL; tokens cached for the session, refreshed with `refresh_token` when they expire, "清除缓存的 token" forgets them). AWSv4/WSSE/NTLM parse and keep their block data but are not yet executed.
- WebSocket requests (Bruno's `meta.type: ws` + `ws { url }` + `body:ws` messages): the tab connects, sends a saved message with variables resolved, and keeps a transcript of sent/received frames; messages are edited in place and saved in Bruno's exact nested-block shape. HTTP requests with `Accept: text/event-stream` stream into the response pane as chunks arrive, and Cancel keeps what was received; collection runs skip both WebSocket and SSE requests.
- Bodies: none, json (including Bruno's canonical `body { ... }` block), text, xml, form-urlencoded, multipart-form (fields whose value is `@file(a.png|b.pdf)` upload those files, paths relative to the collection; `@contentType(type)` sets a part's type), graphql (sent as `{query, variables}` JSON). A body kind Gruno cannot send (`body:file`, `body:sparql`, …) is reported as an invalid request instead of silently sending no body. A request's `settings` block supports `timeout`, `followRedirects`, `maxRedirects` and `encodeUrl: false` (query rows unescaped).
- Cookies: `Set-Cookie` responses are kept for the session and sent back to matching requests (public-suffix rules); "Cookie…" lists, adds, deletes (on every path the cookie was set for) and clears them, and a preference turns the jar off. The response pane can save the body to a file.
- Generate Code (request menu): cURL, JavaScript fetch, Python requests, Go net/http and HTTPie, from the request as it would be sent.
- Scripts run on [goja](https://github.com/dop251/goja): pre-request scripts (collection → folders → request) may change the request before variables resolve; after the response come `vars:post-response`, post-response scripts, the `assert` block and `tests` (request → folders → collection). The response pane's **Tests** tab shows results, script errors and console output. A pre-request script that throws stops the send.
- Script API: `req` (get/set URL, method, headers, body), `res` (`status`, `headers`, parsed `body`, `res('a.b[0]')`, getters), `bru` (`get/setVar`, `get/setEnvVar` with `{persist: true}`, collection/folder/request var getters, `interpolate`, `getProcessEnv`, `sleep`), `test()` (sync or async), chai-style `expect`, `console.*`. Each phase is capped at 10 s.
- Import a Postman v2.0/v2.1 export ("导入 Postman 集合…" on the welcome screen or in the collection menu) as a new collection: folders, requests, bodies, auth (basic, bearer, API key, digest, OAuth2), collection variables. Scripts, file uploads and unsupported auth are skipped and listed.
- Import an OpenAPI 3.x document (JSON or YAML; "导入 OpenAPI 文档…"): operations become requests grouped by their first tag, `servers[0]` becomes `baseUrl`, parameters and body examples are filled in, and security schemes map to auth using `{{token}}`, `{{apiKey}}`, `{{username}}` and `{{password}}`, which the environment must define. Swagger 2.0 is not supported. Callbacks, webhooks, OAuth2 flows and file fields are skipped and listed.
- Collection runner ("运行集合" in the collection menu, "运行目录" on a folder): sends every request in sidebar order with an optional delay, runtime and environment variables carried from one request to the next, and lists each request's status, time and tests (filter by passed / failed / skipped; select one for its tests, response and headers). Scripts steer it with `bru.setNextRequest(name)` / `bru.runner.setNextRequest(name)` (jump to a request by name; `null` ends the run), `bru.runner.skipRequest()` and `bru.runner.stopExecution()`. "数据文件…" loads a CSV (header row) or a JSON array of objects and runs one full pass per row, each column available as `{{column}}`; results show their pass as `#n`, and "导出报告…" writes a JUnit XML report.

## Layout

```
bru/          .bru parser + serializer (dictionary, text, array blocks)
store.go      collection directory: scan, create, save, rename, move, delete
send.go       bru.Request → SendInput adaptation (auth, body, scopes, headers)
request.go    variable resolution + *http.Request construction
http_service.go  send + response classification (30s timeout, 10MB body cap)
digest.go     HTTP Digest auth two-round exchange
app.go        native UI: sidebar tree, tabs, request editor, response pane (Bruno layout: sections Params/Body/Headers/Auth/Vars/Script/Tests/Assert/Docs)
internal/secrets/ encrypted store for environment secret values
internal/script/  goja runtime: Bruno script API, tests, assert operators
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
go test ./...
```

- `bru/` — parser/serializer round-trips, dict/text/array blocks, disabled `~` rows.
- `store_test.go` — collection lifecycle on temp dirs: create/open, CRUD, nested folder vars, env persistence, send adaptation, inherited auth.
- `request_test.go`, `http_service_test.go`, `fixes_test.go`, `oauth2_flow_test.go` — variable resolution (dynamic too), request building, digest with variables, response classification, cookie paths, OAuth2 authorization_code/PKCE against `httptest` servers.
- `app_test.go`, `fixes_test.go` — `ui.NewTester` smoke tests: welcome screen, tree selection, section tabs, send round-trip, row deletion, failed-open state, parse-failure guards, debounced saves, folder settings.
- `runnerdata_test.go` — CSV/JSON data rows, runner iterations and the JUnit report.

## Not yet

- gRPC requests; WebSocket scripts/tests (the connection and messages work; scripts are not run).
- In scripts: `require()` of npm/Node modules, timers, `bru.runRequest`/`sendRequest`.
- AWSv4/WSSE/NTLM signing; digest algorithms other than MD5.
- Insomnia import, Swagger 2.0, collection export; binary file bodies and binary response preview; Postman `pm.*` scripts (not imported).
