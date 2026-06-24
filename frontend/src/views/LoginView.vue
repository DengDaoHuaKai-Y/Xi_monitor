<script setup lang="ts">
import { LockKeyhole, LogIn } from '@lucide/vue';
import { reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../services/api';
import { setToken } from '../services/auth';

const router = useRouter();
const loading = ref(false);
const error = ref('');
const form = reactive({
  username: '',
  password: '',
});

async function submit() {
  loading.value = true;
  error.value = '';

  try {
    const { token } = await api.login(form.username.trim(), form.password);
    setToken(token);
    await router.replace('/dashboard');
  } catch (err) {
    error.value = err instanceof Error ? err.message : '登录失败';
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <main class="login-shell">
    <section class="login-panel">
      <div class="brand-lock">
        <LockKeyhole :size="22" />
      </div>
      <p class="eyebrow">Xi Monitor</p>
      <h1>上游监控面板</h1>
      <p class="login-copy">登录后查看 API 渠道状态、延迟、倍率与余额。</p>

      <form class="login-form" @submit.prevent="submit">
        <label>
          <span>用户名</span>
          <input v-model="form.username" autocomplete="username" />
        </label>
        <label>
          <span>密码</span>
          <input v-model="form.password" type="password" autocomplete="current-password" autofocus />
        </label>
        <p v-if="error" class="form-error">{{ error }}</p>
        <button class="primary-button login-submit" type="submit" :disabled="loading || !form.username || !form.password">
          <LogIn :size="18" />
          {{ loading ? '登录中...' : '登录' }}
        </button>
      </form>
    </section>
  </main>
</template>
