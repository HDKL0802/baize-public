import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// 白泽桌面主控台 Vue3 前端构建配置。
// - 产物输出到 dist/，由 Go 壳 serve（desktop/server.go 指向 dist 目录）。
// - dev 模式把 /api 代理到本机 Go 壳（默认 http://127.0.0.1:8788，见 desktop/server.go），
//   这样前端不用处理跨域与令牌（Go 壳代为注入）。
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // 动态代码分块，避免单包过大
        manualChunks: {
          vue: ['vue', 'vue-router', 'pinia'],
          naive: ['naive-ui'],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      // 本机侧接口（/api/local/*）与后端代理（/api/be/*）都走 Go 壳
      '/api': {
        target: 'http://127.0.0.1:8788',
        changeOrigin: true,
      },
    },
  },
});
