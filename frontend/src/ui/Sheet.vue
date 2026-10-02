<script setup>
// Окно: по центру на компьютере, шторка снизу на телефоне. Esc и клик по фону закрывают.
import { onMounted, onBeforeUnmount } from 'vue'
import Icon from './Icon.vue'
import { t } from '../i18n'

defineProps({ title: { type: String, required: true }, wide: Boolean })
const emit = defineEmits(['close'])
const onKey = (e) => { if (e.key === 'Escape') emit('close') }
onMounted(() => { document.addEventListener('keydown', onKey); document.body.style.overflow = 'hidden' })
onBeforeUnmount(() => { document.removeEventListener('keydown', onKey); document.body.style.overflow = '' })
</script>

<template>
  <div class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm flex items-end sm:items-center justify-center sm:p-4" @click.self="emit('close')">
    <div role="dialog" aria-modal="true" :aria-label="title"
      :class="['bg-surf border border-line w-full shadow-2xl flex flex-col max-h-[92vh] rounded-t-2xl sm:rounded-2xl', wide ? 'sm:max-w-2xl' : 'sm:max-w-md']">
      <div class="flex items-center justify-between gap-3 px-5 pt-4 pb-3">
        <h3 class="text-lg font-semibold">{{ title }}</h3>
        <button type="button" class="icon-btn border-0 bg-transparent" :aria-label="t('close')" @click="emit('close')"><Icon name="close" /></button>
      </div>
      <div class="px-5 pb-4 overflow-y-auto flex flex-col gap-4"><slot /></div>
      <div v-if="$slots.footer" class="px-5 py-4 border-t border-line flex justify-end gap-3 pb-[max(1rem,env(safe-area-inset-bottom))]"><slot name="footer" /></div>
    </div>
  </div>
</template>
