import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import Icons from 'unplugin-icons/vite';

export default defineConfig({
  plugins: [vue(), Icons({ compiler: 'vue3' })],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5175,
    proxy: {
      '/admin': 'http://127.0.0.1:3721',
      '/v1': 'http://127.0.0.1:3721',
    },
  },
});
