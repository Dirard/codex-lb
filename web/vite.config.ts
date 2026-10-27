import path from "node:path";
import { readFileSync, writeFileSync } from "node:fs";
import { readdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { brotliCompress, constants as zlibConstants, gzip } from "node:zlib";

import react from "@vitejs/plugin-react-swc";
import tailwindcss from "@tailwindcss/vite";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

const proxyTarget = process.env.API_PROXY_TARGET || "http://localhost:2455";
const packageJson = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")) as { version?: string };
const appVersion = packageJson.version ?? "0.0.0";
const manualChunkPackages: Record<string, string[]> = {
  "vendor-react": ["react", "react-dom", "react-router-dom"],
  "vendor-query": ["@tanstack/react-query"],
  // recharts is intentionally NOT a manual chunk: forcing it into one made
  // the bundler hoist shared helper modules into that group, which turned
  // the (lazy-only) 580 KB chart bundle into a static import of the entry
  // chunk, modulepreloaded before first paint.
};

function manualChunks(id: string): string | undefined {
  if (!id.includes("/node_modules/")) {
    return undefined;
  }

  for (const [chunkName, packages] of Object.entries(manualChunkPackages)) {
    if (packages.some((packageName) => id.includes(`/node_modules/${packageName}/`))) {
      return chunkName;
    }
  }

  return undefined;
}

// emptyOutDir removes the tracked placeholder that lets `//go:embed all:dist`
// compile in a checkout without built assets; restore it after every build.
function keepGoEmbedPlaceholder(): Plugin {
  return {
    name: "keep-go-embed-placeholder",
    apply: "build",
    closeBundle() {
      writeFileSync(path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../internal/adapters/webui/dist/.gitkeep"), "");
    },
  };
}

// Text types the Go server can label from the original extension; fonts and
// images are already compressed. Tiny files gain nothing from an encoding.
const COMPRESSIBLE_EXTENSIONS = new Set([".html", ".js", ".mjs", ".css", ".svg", ".json", ".xml"]);
const MIN_COMPRESSIBLE_BYTES = 1024;
const brotliAsync = promisify(brotliCompress);
const gzipAsync = promisify(gzip);

// Writes `<file>.br` and `<file>.gz` next to every compressible build output.
// internal/adapters/webui negotiates these per request, so the server never
// compresses static files on the fly. A variant is kept only when smaller.
function precompressAssets(): Plugin {
  let outDir = "";
  let report: (message: string) => void = () => {};
  return {
    name: "precompress-assets",
    apply: "build",
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir);
      report = (message) => config.logger.info(message);
    },
    async closeBundle() {
      const entries = await readdir(outDir, { recursive: true, withFileTypes: true });
      const files = entries
        .filter((entry) => entry.isFile() && COMPRESSIBLE_EXTENSIONS.has(path.extname(entry.name)))
        .map((entry) => path.join(entry.parentPath, entry.name));
      const totals = { files: 0, raw: 0, br: 0, gzip: 0 };
      await Promise.all(
        files.map(async (file) => {
          const source = await readFile(file);
          if (source.length < MIN_COMPRESSIBLE_BYTES) {
            return;
          }
          const [br, gz] = await Promise.all([
            brotliAsync(source, {
              params: {
                [zlibConstants.BROTLI_PARAM_MODE]: zlibConstants.BROTLI_MODE_TEXT,
                [zlibConstants.BROTLI_PARAM_QUALITY]: zlibConstants.BROTLI_MAX_QUALITY,
                [zlibConstants.BROTLI_PARAM_SIZE_HINT]: source.length,
              },
            }),
            gzipAsync(source, { level: zlibConstants.Z_BEST_COMPRESSION }),
          ]);
          totals.files += 1;
          totals.raw += source.length;
          if (br.length < source.length) {
            await writeFile(`${file}.br`, br);
            totals.br += br.length;
          }
          if (gz.length < source.length) {
            await writeFile(`${file}.gz`, gz);
            totals.gzip += gz.length;
          }
        }),
      );
      const kib = (bytes: number) => `${(bytes / 1024).toFixed(1)} KiB`;
      report(`precompressed ${totals.files} files: ${kib(totals.raw)} -> br ${kib(totals.br)}, gzip ${kib(totals.gzip)}`);
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepGoEmbedPlaceholder(), precompressAssets()],
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  resolve: {
    alias: {
      "@": path.resolve(path.dirname(fileURLToPath(import.meta.url)), "./src"),
    },
  },
  server: {
    proxy: {
      "/api": proxyTarget,
      "/v1": proxyTarget,
      "/backend-api": proxyTarget,
      "/health": proxyTarget,
    },
  },
  build: {
    // Emitted into the Go module so `internal/adapters/webui` embeds the SPA
    // into the single server binary. The server never needs a JS runtime.
    outDir: "../internal/adapters/webui/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        manualChunks,
      },
    },
  },
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    testTimeout: 15_000,
    coverage: {
      provider: "v8",
      reporter: ["text", "html"],
      thresholds: {
        lines: 70,
        functions: 70,
        branches: 70,
        statements: 70,
      },
    },
  },
});
