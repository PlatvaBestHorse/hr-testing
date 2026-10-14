<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Library, ClipboardList, Users, ChartNoAxesCombined, ArrowRight, CircleCheck, FileQuestion, Link, BarChart3, LockKeyhole } from '@lucide/vue'
import ConnectionStatus from './ConnectionStatus.vue'
import { api, roleNames, session } from './session'
const props = defineProps<{ page: string }>()
const sections: Record<string, {title: string; description: string; icon: typeof Library; empty: string; detail: string}> = {
  questions: { title: 'Банк вопросов', description: 'Единая библиотека вопросов для ваших тестов.', icon: Library, empty: 'Здесь появятся ваши вопросы', detail: 'Редактор категорий, вариантов ответов и версий вопросов будет доступен на следующем этапе разработки.' },
  tests: { title: 'Тесты', description: 'Проверка знаний начинается с хорошего теста.', icon: ClipboardList, empty: 'Первый тест ещё впереди', detail: 'Конструктор позволит выбрать вопросы, установить время и проходной балл, а затем опубликовать тест.' },
  candidates: { title: 'Кандидаты', description: 'Кандидаты, приглашения и история прохождений.', icon: Users, empty: 'Пока нет кандидатов', detail: 'Здесь можно будет создавать карточки кандидатов и выдавать персональные ссылки на тесты.' },
  results: { title: 'Результаты', description: 'Всё, что нужно для осмысленной оценки.', icon: ChartNoAxesCombined, empty: 'Результаты появятся после прохождения', detail: 'Итоговые баллы, ответы и отчёты будут доступны после реализации прохождения тестов.' },
}
const section = computed(() => sections[props.page])
const metrics = [ { label: 'Вопросы в банке', icon: Library }, { label: 'Опубликованные тесты', icon: ClipboardList }, { label: 'Кандидаты', icon: Users }, { label: 'Завершённые попытки', icon: ChartNoAxesCombined } ]
const counts = ref<number[] | null>(null), summaryError = ref('')
watch(() => props.page, async () => {
  if (props.page !== 'dashboard' || session.user?.role === 'assessor') return
  counts.value = null; summaryError.value = ''
  try { const data = await api<{ questions: number; tests: number; candidates: number; attempts: number }>('/summary'); counts.value = [data.questions, data.tests, data.candidates, data.attempts] }
  catch (err) { summaryError.value = (err as Error).message }
}, { immediate: true })
const steps = [
  { number: '01', title: 'Подготовьте вопросы', text: 'Добавьте задания и укажите правильные ответы.', icon: FileQuestion, path: '/questions' },
  { number: '02', title: 'Соберите тест', text: 'Выберите вопросы, время и правила оценивания.', icon: ClipboardList, path: '/tests' },
  { number: '03', title: 'Пригласите кандидата', text: 'Отправьте персональную ссылку на прохождение.', icon: Link, path: '/candidates' },
  { number: '04', title: 'Изучите результат', text: 'Посмотрите баллы и подробности прохождения.', icon: BarChart3, path: '/results' },
]
</script>

<template>
  <template v-if="page === 'dashboard'">
    <div class="page-heading"><div><p class="eyebrow">ВАШ ЦЕНТР ОЦЕНКИ</p><h1>Всё начинается с людей<span class="heading-dot">.</span></h1><p class="subtitle">Вопросы, тесты и кандидаты — в одном рабочем пространстве.</p></div><span class="outline-badge">Первый шаг</span></div>
    <section class="welcome-card"><div class="welcome-content"><span class="welcome-tag"><span></span>НОВОЕ ПРОСТРАНСТВО</span><h2>Хороший найм начинается<br class="desktop-break" /> с правильных вопросов</h2><p>Создавайте тесты, приглашайте кандидатов и получайте<br class="desktop-break" /> результаты, на которые можно опереться.</p><RouterLink to="/questions" class="primary-link">Посмотреть банк вопросов <ArrowRight :size="18" /></RouterLink><span class="welcome-caption">Подготовьте банк вопросов для первых тестов.</span></div><div class="welcome-art" aria-hidden="true"><div class="art-orbit"></div><div class="art-paper"><div class="art-paper-top"><span></span><span></span><span></span></div><div class="art-line wide"></div><div class="art-line"></div><div class="art-answer"><CircleCheck :size="19" /><span></span></div><div class="art-answer muted"><span class="art-circle"></span><span></span></div><div class="art-answer muted"><span class="art-circle"></span><span></span></div></div><div class="art-check"><CircleCheck :size="26" /></div><div class="art-star">✦</div></div></section>
    <p v-if="summaryError" class="form-error" role="alert">{{ summaryError }}</p>
    <section v-if="session.user?.role !== 'assessor'" class="metrics" aria-label="Сводка"><article v-for="(metric, index) in metrics" :key="metric.label" class="metric-card"><div><span>{{ metric.label }}</span><component :is="metric.icon" :size="19" /></div><strong>{{ counts ? counts[index] : '—' }}</strong><span class="metric-note">{{ counts ? 'В вашей организации' : 'Загружаем данные' }}</span></article></section>
    <section class="start-section"><div class="section-heading"><div><h2>Путь от вопроса до результата</h2><p>Четыре шага к первой оценке кандидата.</p></div><span class="small-label">КАК ЭТО БУДЕТ РАБОТАТЬ</span></div><div class="steps"><RouterLink v-for="step in steps" :key="step.number" :to="step.path" class="step-card"><div class="step-top"><span class="step-number">{{ step.number }}</span><component :is="step.icon" :size="21" /></div><h3>{{ step.title }}</h3><p>{{ step.text }}</p><span class="step-link">Открыть раздел <ArrowRight :size="15" /></span></RouterLink></div></section>
    <div class="bottom-strip"><div class="privacy-note"><LockKeyhole :size="18" /><span>Тестовые данные отдельно от реальных кандидатов</span></div><ConnectionStatus /></div>
  </template>
  <template v-else-if="section">
    <div class="page-heading"><div><p class="eyebrow">РАБОЧЕЕ ПРОСТРАНСТВО</p><h1>{{ section.title }}</h1><p class="subtitle">{{ section.description }}</p></div><span class="outline-badge">В разработке</span></div>
    <section class="empty-state"><div class="empty-icon"><component :is="section.icon" :size="32" /></div><h2>{{ section.empty }}</h2><p>{{ section.detail }}</p><RouterLink to="/dashboard" class="primary-link">Вернуться к обзору <ArrowRight :size="18" /></RouterLink></section>
  </template>
  <template v-else-if="page === 'settings'">
    <div class="page-heading"><div><p class="eyebrow">РАБОЧЕЕ ПРОСТРАНСТВО</p><h1>Настройки</h1><p class="subtitle">Состояние подключения и подготовка рабочего пространства.</p></div></div>
    <section class="settings-card"><h2>Подключение</h2><p>Проверка доступности сервера и подготовленной базы данных.</p><ConnectionStatus /></section>
    <section class="settings-card"><h2>Организация и доступ</h2><p>{{ session.user?.organization_name }}</p><p>{{ session.user?.display_name }} · {{ session.user && roleNames[session.user.role] }}</p><RouterLink v-if="session.user?.role === 'org_admin'" to="/team" class="primary-link">Управлять командой <ArrowRight :size="17" /></RouterLink></section>
  </template>
  <section v-else class="empty-state"><h1>Страница не найдена</h1><p>Проверьте адрес или вернитесь в рабочее пространство.</p><RouterLink to="/dashboard" class="primary-link">К обзору <ArrowRight :size="18" /></RouterLink></section>
</template>
