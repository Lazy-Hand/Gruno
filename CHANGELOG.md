# Changelog

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
