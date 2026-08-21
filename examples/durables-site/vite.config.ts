import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const buildID = process.env.GOBEYOND_BUILD_ID ?? "development";
const clientEntry = process.env.GOBEYOND_CLIENT_ENTRY ?? "client.tsx";
const outDir =
  process.env.GOBEYOND_STATIC_OUT ??
  `../../dist/static/_gobeyond/builds/${buildID}/assets`;

export default defineConfig({
  plugins: [react()],
  publicDir: false,
  build: {
    outDir,
    emptyOutDir: false,
    sourcemap: false,
    rollupOptions: {
      input: clientEntry,
      output: {
        entryFileNames: "app.js",
        chunkFileNames: "chunks/[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
  },
});
