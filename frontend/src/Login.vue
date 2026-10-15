<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Layers, ArrowRight, LockKeyhole } from '@lucide/vue'
import { login, ApiError } from './session'
import ConnectionStatus from './ConnectionStatus.vue'
const router = useRouter()
const route = useRoute(), secondFactor = ref(''), mfaRequired = ref(false)
const email = ref(''), password = ref(''), error = ref(''), pending = ref(false)
async function submit() {
  error.value = ''; pending.value = true
  try { await login(email.value, password.value, secondFactor.value); password.value = ''; secondFactor.value = ''; await router.replace('/dashboard') }
  catch (err) { if (err instanceof ApiError && err.code === 'mfa_required') mfaRequired.value = true; error.value = err instanceof Error ? err.message : 'Не удалось войти' }
  finally { pending.value = false }
}
</script>
<template>
  <main class="login-page">
    <div class="login-brand"><span class="brand-icon"><Layers :size="25" /></span><strong>Кадровый тестер <span>2.0</span></strong></div>
    <section class="login-card">
      <p class="eyebrow">РАБОЧЕЕ ПРОСТРАНСТВО HR</p><h1>Рады видеть вас</h1><p class="subtitle">Войдите, чтобы управлять вопросами и тестами.</p>
      <form @submit.prevent="submit">
        <p v-if="route.query.security === 'updated'" class="form-success" role="status">Настройки безопасности изменены. Войдите снова.</p>
        <label class="form-field">Электронная почта<input v-model="email" type="email" autocomplete="username" required maxlength="320" placeholder="name@company.ru" /></label>
        <label class="form-field">Пароль<input v-model="password" type="password" autocomplete="current-password" required maxlength="1024" /></label>
        <label v-if="mfaRequired" class="form-field">Код приложения или резервный код<input v-model="secondFactor" autocomplete="one-time-code" required maxlength="64" /></label>
        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        <button class="primary-link" :disabled="pending" type="submit">{{ pending ? 'Входим…' : 'Войти' }}<ArrowRight :size="18" /></button>
      </form>
      <RouterLink to="/forgot-password" class="back-link">Забыли пароль?</RouterLink>
      <p class="login-note"><LockKeyhole :size="16" />Доступ к организации выдаёт администратор.</p>
    </section>
    <ConnectionStatus />
  </main>
</template>
