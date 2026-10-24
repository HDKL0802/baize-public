import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: 'dist',
    rollupOptions: {
      output: {
        manualChunks: {
          vue: ['vue'],
          naive: ['naive-ui'],
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:47411',
    },
  },
});
