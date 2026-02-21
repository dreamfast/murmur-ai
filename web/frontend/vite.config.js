import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    outDir: "../dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/ws": {
        target: "http://localhost:8082",
        ws: true,
      },
      "/dashboard": {
        target: "http://localhost:8082",
      },
    },
  },
});
