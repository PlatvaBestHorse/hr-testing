import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        configure(proxy) {
          proxy.on('proxyReq', (outgoing, request) => {
            outgoing.setHeader('X-Forwarded-For', request.socket.remoteAddress || '127.0.0.1')
            outgoing.removeHeader('X-Real-IP')
            outgoing.removeHeader('Forwarded')
          })
        },
      },
    },
  },
  test: { environment: 'happy-dom', include: ['src/**/*.test.ts'] },
})
