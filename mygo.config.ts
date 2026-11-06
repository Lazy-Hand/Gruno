import { defineConfig } from "mygo-cli";

// Native-UI app: no web frontend, no bindings, no dev server. The Go
// side draws every window with package ui.
export default defineConfig({
  name: "gruno",
  identifier: "com.example.gruno",
  version: "0.1.0",
  out: "build",
});
