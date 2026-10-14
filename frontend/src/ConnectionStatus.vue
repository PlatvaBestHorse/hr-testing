<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RefreshCw } from '@lucide/vue'
const state = ref<'checking' | 'ready' | 'unavailable'>('checking')
let controller: AbortController | undefined
let disposed = false
async function check() {
  controller?.abort()
  controller = new AbortController()
  const current = controller
  state.value = 'checking'
  const timer = setTimeout(() => current.abort(), 4000)
  try {
    const response = await fetch('/api/v1/status', { signal: current.signal, cache: 'no-store' })
    const body = await response.json()
    if (!disposed && current === controller) state.value = response.ok && body.status === 'ready' ? 'ready' : 'unavailable'
  } catch {
    if (!disposed && current === controller) state.value = 'unavailable'
  } finally { clearTimeout(timer) }
}
onMounted(check)
onUnmounted(() => { disposed = true; controller?.abort() })
</script>

<template>
  <div class="connection" :class="state">
    <span class="connection-light"></span>
    <span role="status">{{ state === 'ready' ? 'Сервер и база данных доступны' : state === 'checking' ? 'Проверяем соединение…' : 'Не удалось подключиться к серверу или базе данных' }}</span>
    <button class="icon-button" :disabled="state === 'checking'" aria-label="Проверить соединение" @click="check"><RefreshCw :size="16" /></button>
  </div>
</template>
