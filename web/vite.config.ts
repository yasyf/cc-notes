import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies /api (including the /api/stream SSE endpoint, which
// http-proxy streams unbuffered by default) to the local cc-notes viz server.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:5177",
        changeOrigin: true,
        ws: false,
      },
    },
  },
});
