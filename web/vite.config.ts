// defineConfig 必须从 vitest/config 导入，不能从 vite 导入。
// 从 vite 导入时，下面的 test 配置块会让 tsc -b 报 TS2769。
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig(({ command }) => ({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  // 生产构建时，index.html 里引用的资源地址（<script src>/<link href>）
  // 都会带上这个前缀。默认 /static/：fp 自己的 internal/httpapi/static.go
  // 就是把构建产物挂在这个前缀下的，不配 CDN 也能直接跑。要接 CDN 回源
  // （比如 static.xxzj.com/fp/ 指到源站的 /static/），把
  // VITE_ASSET_BASE 设成 CDN 那边的完整 URL 再构建，index.html 里的资源
  // 地址就会直接指向 CDN 域名。
  //
  // 只在 build 时生效（command === 'build'）：dev server（vite dev）不吃
  // 这个前缀，还是服务在根路径，不然本机开发打开 http://localhost:5173
  // 会直接 404，得改成带 /static/ 的地址才能访问，平白破坏现有的开发体验。
  base: command === 'build' ? (process.env.VITE_ASSET_BASE || '/static/') : '/',
  server: {
    // 监听 0.0.0.0：同一局域网内的其他设备（手机、另一台机器）也能通过
    // 本机 IP 访问这个开发服务器，不仅限于 localhost。
    host: '0.0.0.0',
    // 开发时把 API 转发给本机的 fp，浏览器看到的仍是同源，
    // 管理端会话 cookie 因此能正常带上。
    proxy: { '/admin/api': 'http://localhost:8080' },
  },
  test: {
    environment: 'jsdom',
    // 刻意不开 globals：它只影响运行时，TypeScript 依然不认识 test/expect，
    // tsc -b 会报 TS2593/TS2304。每个测试文件顶部显式 import 即可。
    globals: false,
    // @testing-library/react 的自动清理靠探测全局 afterEach 触发，globals
    // 关闭后探测不到，组件测试之间不会自动 unmount（详见 test-setup.ts 里的
    // 注释）。这里手动接上，否则每个用到 render() 的测试文件都要重复踩坑。
    setupFiles: ['./src/test-setup.ts'],
  },
}))
