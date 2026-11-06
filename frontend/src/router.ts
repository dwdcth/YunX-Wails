import { createRouter, createWebHashHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/resolve' },
    {
      path: '/resolve',
      component: () => import('./views/ResolveView.vue'),
      meta: { title: '解析', icon: 'link' },
    },
    {
      path: '/drive',
      component: () => import('./views/DriveView.vue'),
      meta: { title: '网盘', icon: 'cloud' },
    },
    {
      path: '/video',
      component: () => import('./views/VideoView.vue'),
      meta: { title: '视频', icon: 'play' },
    },
    {
      path: '/download',
      component: () => import('./views/DownloadView.vue'),
      meta: { title: '下载', icon: 'download' },
    },
    {
      path: '/settings',
      component: () => import('./views/SettingsView.vue'),
      meta: { title: '设置', icon: 'settings' },
    },
    // 隐藏：书签页（入口 = 顶栏五角星按钮）
    {
      path: '/bookmarks',
      component: () => import('./views/BookmarksView.vue'),
      meta: { title: '书签', icon: 'star' },
    },
    // 隐藏：关于页（入口 = 设置页底部「关于」按钮）
    {
      path: '/about',
      component: () => import('./views/AboutView.vue'),
      meta: { title: '关于' },
    },
    // 隐藏：链路冒烟页（原占位页升级版，保留给巡检/联调用）
    { path: '/smoke', component: () => import('./views/SmokeView.vue') },
  ],
})
