import { createRouter, createWebHashHistory } from 'vue-router';

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'overview', component: () => import('./views/OverviewView.vue'), meta: { title: '总览' } },
    { path: '/providers', name: 'providers', component: () => import('./views/ProvidersView.vue'), meta: { title: '供应商' } },
    { path: '/channels', name: 'channels', component: () => import('./views/ChannelsView.vue'), meta: { title: '路由' } },
    { path: '/sessions', name: 'sessions', component: () => import('./views/SessionsView.vue'), meta: { title: '会话' } },
    { path: '/pricing', name: 'pricing', component: () => import('./views/PricingView.vue'), meta: { title: '定价' } },
    { path: '/logs', name: 'logs', component: () => import('./views/LogsView.vue'), meta: { title: '日志' } },
    { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue'), meta: { title: '设置' } },
    { path: '/guide', name: 'guide', component: () => import('./views/GuideView.vue'), meta: { title: '指引' } },
  ],
});

router.afterEach((to) => {
  document.title = 'cr-' + String(to.meta.title ?? '');
});
