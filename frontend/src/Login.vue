<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Layers, ArrowRight, LockKeyhole } from '@lucide/vue'
import { login } from './session'
import ConnectionStatus from './ConnectionStatus.vue'
const router = useRouter()
const email = ref(''), password = ref(''), error = ref(''), pending = ref(false)
async function submit() {
  error.value = ''; pending.value = true
  try { await login(email.value, password.value); password.value = ''; await router.replace('/dashboard') }
  catch (err) { error.value = err instanceof Error ? err.message : 'Не удалось войти' }
  finally { pending.value = false }
}
</script>
<template>
  <main class="login-page">
    <div class="login-brand"><span class="brand-icon"><Layers :size="25" /></span><strong>Кадровый тестер <span>2.0</span></strong></div>
    <section class="login-card">
      <p class="eyebrow">РАБОЧЕЕ ПРОСТРАНСТВО HR</p><h1>Рады видеть вас</h1><p class="subtitle">Войдите, чтобы управлять вопросами и тестами.</p>
      <form @submit.prevent="submit">
        <label class="form-field">Электронная почта<input v-model="email" type="email" autocomplete="username" required maxlength="320" placeholder="name@company.ru" /></label>
        <label class="form-field">Пароль<input v-model="password" type="password" autocomplete="current-password" required maxlength="1024" /></label>
        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        <button class="primary-link" :disabled="pending" type="submit">{{ pending ? 'Входим…' : 'Войти' }}<ArrowRight :size="18" /></button>
      </form>
      <p class="login-note"><LockKeyhole :size="16" />Доступ к организации выдаёт администратор.</p>
    </section>
    <ConnectionStatus />
  </main>
</template>
