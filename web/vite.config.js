import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物直接写入 internal/web/dist，由 Go 的 //go:embed 打包进单二进制。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    target: 'es2020',
    assetsDir: 'assets',
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5173,
    host: '127.0.0.1',
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/s3': 'http://127.0.0.1:8080',
      '/mcp': 'http://127.0.0.1:8080',
    },
  },
})
