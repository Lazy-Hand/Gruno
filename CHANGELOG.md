# Changelog

## [0.2.1] - 2026-11-09

- Variables live inside the fields: a {{name}} shows as a chip —
  accent when it resolves, red when it does not — whose tooltip has the
  value and whose menu swaps it for another variable. Typing "{{" in a
  field lists the variables, filtered as you type; ↑/↓ and Enter or
  Tab insert one. Request bodies complete variables the same way. The
  {} buttons beside the fields are gone
- Cleaner request and response panes: the response status, time and
  size no longer wrap and copy/clear stay in view; section tabs scroll
  in a narrow pane; the Tests tab shows passed/total in green or red
- The timeline shows the request as sent, variables resolved and auth
  attached
- The sidebar shortens DELETE and OPTIONS to DEL and OPT
- Multipart form bodies are sent (they were silently dropped before),
  with file uploads: "添加文件…" adds a @file(...) field, paths inside
  the collection kept relative
- Cookies: responses' Set-Cookie are kept for the session and sent with
  later requests to the same site; "Cookie…" in the collection menu and
  Preferences lists, deletes and clears them, and Preferences can turn
  this off
- Generate Code from a request's menu: cURL, JavaScript fetch, Python
  requests, Go net/http and HTTPie, variables resolved and auth attached
- Collection runner: "运行集合" in the collection menu or "运行目录" on a
  folder opens a runner tab that sends the requests in order, with an
  optional delay, carrying runtime and environment variables along; each
  request shows its status, time and tests, and a failed one its details.
  bru.setNextRequest, bru.runner.setNextRequest, skipRequest and
  stopExecution now steer the run instead of being ignored
- Import a Postman collection (v2.0/v2.1 JSON) as a new collection, from
  the welcome screen or the collection menu. Folders, requests, bodies,
  auth and collection variables carry over; scripts, file uploads and
  unsupported auth are skipped and listed when the import finishes
- Import an OpenAPI 3.x document (JSON or YAML) as a new collection:
  operations are grouped into folders by their first tag, servers become
  the baseUrl variable, examples fill parameters and bodies, and security
  schemes become auth with {{token}}, {{apiKey}}, {{username}} and
  {{password}} variables for the environment to define
## [0.2.0] - 2026-11-08

- Scripts run: pre-request and post-response scripts at collection,
  folder and request level, with Bruno's req/res/bru API on a built-in
  JavaScript engine
- Tests and assertions run after each response; the response pane's
  new Tests tab shows passed/failed results, script errors and console
  output
- Runtime variables (bru.setVar, vars:post-response) carry into later
  sends and appear in the {} variable menu; bru.setEnvVar updates the
  active environment, writing it with {persist: true}
- {{process.env.NAME}} reads the collection's .env file
- Environment secrets: mark a variable as Secret in the environment
  editor and its value stays on this Mac, encrypted, with the key in
  the Keychain; the .bru file keeps only the name, as Bruno does.
  Secret values resolve in requests and scripts
- OAuth2 auth for requests and collections: client_credentials and
  password grants, token cached and refreshed automatically, sent in
  the Authorization header or the URL; the timeline says whether a
  token was fetched, refreshed or reused
- Network settings: Preferences (⌘,) for TLS certificate verification,
  an extra CA certificate and the default timeout; a collection's proxy
  (http, https, socks5, with auth and bypass list) and client
  certificates (PEM or PFX by domain); per-request timeout and
  redirect settings
- Saving no longer drops what Gruno does not understand: unknown
  bruno.json keys and unknown .bru blocks (such as auth:awsv4 or
  settings) are kept as they were

## [0.1.2] - 2026-11-07

- Variable insertion, Apifox-style: a {} icon on every interpolatable
  input (URL, params, headers, auth fields, env vars) and on each code
  editor's toolbar (body, GraphQL, scripts, tests, docs). Picking a
  variable inserts {{name}} into that field, at the caret in editors,
  completing a typed {{ prefix elsewhere
- The menu lists variables in Bruno's resolution order: collection,
  folder, active environment, request

## [0.1.1] - 2026-11-06

- In-app updates: a Sparkle-style window checks GitHub Releases in the
  background and daily, and offers to install the new version, later or
  never ("Check for Updates…" in the app menu for a manual check)
- The window title bar now follows the app's light/dark theme instead of
  the system's, and matches the content's color
- The status bar shows the real app version

## [0.1.0] - 2026-11-06

- First release: a Bruno workbench for macOS on mygo's native UI
- Opens Bruno collections directly; every edit writes the `.bru` file
  live, like Bruno itself
- Sends requests over Go's HTTP stack: params, bodies (JSON/text/XML,
  form-urlencoded, multipart, GraphQL), headers, auth (basic, bearer,
  API key, digest), collection/folder/environment `{{var}}` resolution
- Sidebar tree, request tabs, per-section editors, response pane with
  pretty/raw views, cookies and timeline
- Light/dark themes following the system or pinned
