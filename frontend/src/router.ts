import { createRouter, createWebHistory } from 'vue-router';
import { getToken } from './services/auth';
import DashboardView from './views/DashboardView.vue';
import LoginView from './views/LoginView.vue';

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/login', component: LoginView, meta: { public: true } },
    { path: '/dashboard', component: DashboardView },
  ],
});

router.beforeEach((to) => {
  if (to.meta.public) {
    if (to.path === '/login' && getToken()) return '/dashboard';
    return true;
  }

  if (!getToken()) return '/login';
  return true;
});
