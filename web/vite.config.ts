import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8000', '/auth': 'http://localhost:8000' },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: (id: string) => (/node_modules\/(react|react-dom|scheduler|react-router|react-router-dom)\//.test(id) ? 'vendor' : undefined),
      },
    },
  },
  test: { environment: 'jsdom' },
} as never);
