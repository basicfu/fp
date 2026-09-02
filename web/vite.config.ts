// defineConfig 必须从 vitest/config 导入，不能从 vite 导入。
// 从 vite 导入时，下面的 test 配置块会让 tsc -b 报 TS2769。
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    // 开发时把 API 转发给本机的 fp，浏览器看到的仍是同源，
    // 管理端会话 cookie 因此能正常带上。
    proxy: { '/admin/api': 'http://localhost:8080' },
  },
  test: {
    environment: 'jsdom',
    // 刻意不开 globals：它只影响运行时，TypeScript 依然不认识 test/expect，
    // tsc -b 会报 TS2593/TS2304。每个测试文件顶部显式 import 即可。
    globals: false,
  },
})
