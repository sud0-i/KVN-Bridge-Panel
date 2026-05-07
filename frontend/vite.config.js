import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      // Все запросы, начинающиеся на /api, Vite будет сам пересылать на наш Go-сервер
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      }
    }
  }
})