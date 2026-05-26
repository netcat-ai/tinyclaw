import { defineConfig } from 'vite'
import Vue from '@vitejs/plugin-vue'
import UnoCSS from 'unocss/vite'

export default defineConfig({
  base: '/admin/',
  plugins: [Vue(), UnoCSS()],
  server: {
    proxy: {
      '/admin/api': 'http://127.0.0.1:8081',
      '/api': 'http://127.0.0.1:8081',
    },
  },
})
