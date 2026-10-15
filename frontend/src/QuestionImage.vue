<script setup lang="ts">
import { ref, watch } from 'vue'
const props = defineProps<{ src: string; alt: string }>()
const failed = ref(false), retry = ref(0)
watch(() => props.src, () => { failed.value = false; retry.value = 0 })
</script>
<template>
  <div class="question-image">
    <template v-if="failed"><p class="muted-text" role="alert">Изображение не загрузилось. Проверьте соединение.</p><button type="button" class="text-button" @click="failed = false; retry++">Загрузить изображение повторно</button></template>
    <img v-else :src="src + (retry ? '?retry=' + retry : '')" :alt="alt" referrerpolicy="no-referrer" @error="failed = true" />
  </div>
</template>
