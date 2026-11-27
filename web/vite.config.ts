import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

import solid from "@solidjs/vite-plugin";
import { defineConfig, type Plugin } from "vite";

import { mockApi } from "./mock/plugin.ts";

// The Go daemon embeds web/dist and keeps dist/.gitkeep checked in so a
// checkout without a build still compiles. emptyOutDir removes it, so the
// build puts it back.
function keepGitkeep(): Plugin {
  let outDir = "";
  return {
    name: "anvil:keep-gitkeep",
    apply: "build",
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      writeFileSync(resolve(outDir, ".gitkeep"), "");
    },
  };
}

export default defineConfig(({ command }) => {
  const mock = command === "serve" && process.env.VITE_MOCK === "1";
  return {
    plugins: [solid(), keepGitkeep(), mock && mockApi({ auth: process.env.VITE_MOCK_AUTH === "1" })],
    // ui-sdk throws in development when its core is pre-bundled apart from the
    // copy @ui-sdk/solid loads.
    optimizeDeps: { exclude: ["@ui-sdk/core"] },
    server: {
      port: 5173,
      proxy: mock ? undefined : { "/api": process.env.ANVIL_API ?? "http://127.0.0.1:8484" },
    },
    build: {
      outDir: "dist",
      emptyOutDir: true,
      sourcemap: false,
      target: "es2022",
    },
  };
});
