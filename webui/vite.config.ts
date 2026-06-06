import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
          'vendor-antd':  ['antd'],
          'vendor-icons': ['@ant-design/icons'],
          'vendor-utils': ['dayjs', 'axios', 'js-yaml'],
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8081',
    },
  },
})
