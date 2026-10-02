<script setup>
// Сегментированный выбор. options: [{ value, label, disabled? }]
const model = defineModel()
defineProps({ options: { type: Array, required: true }, label: { type: String, required: true } })
</script>

<template>
  <div role="radiogroup" :aria-label="label" class="inline-flex flex-wrap gap-0.5 p-[3px] rounded-lg bg-surf2 border border-line max-w-full">
    <button v-for="o in options" :key="o.value" type="button" role="radio" :aria-checked="model === o.value" :disabled="o.disabled"
      @click="model = o.value"
      :class="['h-9 px-3 rounded-md text-[13px] whitespace-nowrap transition-colors disabled:opacity-40 disabled:cursor-not-allowed', model === o.value ? 'bg-surf text-fg font-medium shadow-sm' : 'text-mute hover:text-fg']">
      {{ o.label }}
    </button>
  </div>
</template>
