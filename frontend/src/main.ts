
// ---- 前端错误回传：window.onerror / unhandledrejection → 插件桥 /log → Go 日志 ----
// Vue 未捕获渲染错误会让组件树整块卸载（界面空白），没有这个探针就只能在截图里猜。
function reportFrontend(kind: string, msg: string) {
  try {
    fetch('http://127.0.0.1:37021/log', { method: 'POST', body: kind + ' ' + msg, mode: 'no-cors' })
  } catch { /* fire-and-forget */ }
}
window.addEventListener('error', (e) => {
  reportFrontend('JS-ERROR', `${e.message} @ ${e.filename}:${e.lineno}:${e.colno}`)
})
window.addEventListener('unhandledrejection', (e) => {
  reportFrontend('PROMISE-REJECT', String(e.reason))
})
import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { initTheme } from './theme'
import './styles.css'

initTheme()
createApp(App).use(router).mount('#app')
