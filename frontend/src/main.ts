
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
// console.error 劫持：Vue 的渲染/警告错误走 console 而非 window error，必须在这层截
const origError = console.error.bind(console)
console.error = (...args: unknown[]) => {
  reportFrontend('CONSOLE-ERROR', args.map((a) => {
    if (a instanceof Error) return `${a.name}: ${a.message}\n${(a.stack || '').slice(0, 1200)}`
    return String(a)
  }).join(' | ').slice(0, 2000))
  origError(...args)
}
window.addEventListener('unhandledrejection', (e) => {
  reportFrontend('PROMISE-REJECT', String(e.reason))
})
import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { initTheme } from './theme'
import './styles.css'

initTheme()
const app = createApp(App)
app.config.errorHandler = (err, _instance, info) => {
  reportFrontend('VUE-ERROR', `${String(err)} [${info}]`)
}
app.use(router).mount('#app')
