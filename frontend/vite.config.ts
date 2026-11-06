import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// base 用相对路径：产物嵌进 wails 资产服务后在任意挂载路径下都可用。
// /wails/runtime.js 由 wails 运行时在应用内提供（wails3 generate bindings -b 的内置 runtime），
// 构建期必须 external，否则 rollup 解析不到绝对路径会报错。
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: 'dist',
    rollupOptions: { external: [/^\/wails\//] },
  },
})
