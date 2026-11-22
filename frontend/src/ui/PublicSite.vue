<script setup>
import { onMounted, ref } from 'vue'
import { t } from '../i18n'
import { apiCall, readError } from '../store'
import Sheet from './Sheet.vue'

const props = defineProps({ node: { type: String, default: '' } })
defineEmits(['close'])

const state = ref(null)
const page = ref(null)
const preview = ref('')
const busy = ref(false)
const error = ref('')
const message = ref('')
const endpoint = 'api/public-site' + (props.node ? '?node=' + encodeURIComponent(props.node) : '')

async function load(method, input) {
  const response = await apiCall(endpoint, { method, body: input ? JSON.stringify(input) : undefined })
  if (!response.ok) throw new Error(await readError(response))
  state.value = await response.json()
  page.value = { ...state.value.current }
}

async function apply(action) {
  busy.value = true
  error.value = message.value = ''
  try {
    if (action === 'load') {
      await load('GET')
    } else if (action === 'preview') {
      const response = await apiCall('api/public-site/preview', { method: 'POST', body: JSON.stringify(page.value) })
      if (!response.ok) throw new Error(await readError(response))
      preview.value = await response.text()
    } else {
      await load('PUT', { page: page.value, rollback: action === 'rollback' })
      preview.value = ''
      message.value = t('site.published')
    }
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}

onMounted(() => apply('load'))
</script>

<template>
  <Sheet :title="t('site.title')" wide @close="!busy && $emit('close')">
    <p class="hint">{{ t(node ? 'site.nodeHint' : 'site.panelHint') }}</p>
    <fieldset v-if="page" :disabled="busy" class="flex flex-col gap-3">
      <label class="label">{{ t('site.html') }}
        <textarea v-model="page.html" rows="10" class="input mt-1 font-mono"
          placeholder="<!doctype html>" spellcheck="false" aria-describedby="public-site-html-hint"
          @input="preview = ''" />
      </label>
      <p id="public-site-html-hint" class="hint">{{ t('site.htmlHint') }}</p>
      <details v-if="!page.html?.trim()" class="rounded-xl border border-line p-3">
        <summary class="cursor-pointer text-sm">{{ t('site.template') }}</summary>
        <div class="mt-3 flex flex-col gap-3">
          <label class="label">{{ t('site.heading') }}
            <input v-model="page.title" maxlength="80" class="input mt-1" @input="preview = ''">
          </label>
          <label class="label">{{ t('site.intro') }}
            <textarea v-model="page.intro" maxlength="300" rows="3" class="input mt-1" @input="preview = ''" />
          </label>
          <label class="label">{{ t('site.theme') }}
            <select v-model="page.theme" class="input mt-1" @change="preview = ''">
              <option value="paper">{{ t('site.paper') }}</option>
              <option value="slate">{{ t('site.slate') }}</option>
            </select>
          </label>
        </div>
      </details>
    </fieldset>
    <iframe v-if="preview" :srcdoc="preview" :title="t('site.preview')" sandbox class="h-72 w-full rounded-xl border border-line" />
    <p v-if="busy" role="status" class="hint">{{ t('loading') }}</p>
    <p v-if="error" role="alert" class="text-sm text-err">{{ error }}</p>
    <p v-if="message" role="status" class="text-sm text-ok">{{ message }}</p>
    <template #footer>
      <button v-if="state?.previous" type="button" class="btn btn-secondary" :disabled="busy" @click="apply('rollback')">{{ t('site.rollback') }}</button>
      <button v-if="page" type="button" class="btn btn-secondary" :disabled="busy" @click="apply('preview')">{{ t('site.preview') }}</button>
      <button v-if="page" type="button" class="btn btn-primary" :disabled="busy" @click="apply('publish')">{{ t('site.publish') }}</button>
    </template>
  </Sheet>
</template>
