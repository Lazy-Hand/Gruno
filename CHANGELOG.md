# Changelog

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
