<script setup>
import Icon from '../ui/Icon.vue'
import { t } from '../i18n'
import { overview, attention, topUsers, bridges, exits, hasExits, nodeName, fmtGB, used, routing, openUserModal, users, linkTo, links } from '../store'

defineEmits(['go'])

const protoLine = () => {
  const r = routing.value
  if (!r) return ''
  return ['VLESS', r.xhttp && 'XHTTP', r.hysteria && 'Hysteria2', r.mieru && 'mieru', r.cdn && 'CDN'].filter(Boolean).join(' · ')
}
const dot = (n) => (!n.IsOnline ? 'bg-err' : n.ConfigError || n.UpdateError || n.SNIError ? 'bg-warn' : 'bg-ok')
const attIcon = { err: 'text-err', warn: 'text-warn', acc: 'text-acc' }
</script>

<template>
  <div class="flex flex-col gap-4 sm:gap-6">
    <!-- Цифры -->
    <div class="grid grid-cols-2 xl:grid-cols-4 gap-3 sm:gap-4">
      <div class="card gap-1.5 sm:gap-2">
        <span class="text-xs sm:text-[13px] text-mute">{{ t('ov.nodesOnline') }}</span>
        <span :class="['text-2xl sm:text-[30px] font-semibold tabular-nums tracking-tight', overview.nodesOnline === overview.nodesTotal ? 'text-ok' : 'text-warn']">
          {{ overview.nodesOnline }} / {{ overview.nodesTotal }}</span>
        <span class="text-xs sm:text-[13px] text-dim">{{ t('ov.nodesNote', { b: bridges.length, e: exits.length }) }}</span>
      </div>
      <div class="card gap-1.5 sm:gap-2">
        <span class="text-xs sm:text-[13px] text-mute">{{ t('ov.users') }}</span>
        <span class="text-2xl sm:text-[30px] font-semibold tabular-nums tracking-tight">{{ overview.usersActive }}</span>
        <span class="text-xs sm:text-[13px] text-dim">{{ t('ov.usersNote', { n: overview.usersTotal }) }}</span>
      </div>
      <div class="card gap-1.5 sm:gap-2">
        <span class="text-xs sm:text-[13px] text-mute">{{ t('ov.traffic') }}</span>
        <span class="text-2xl sm:text-[30px] font-semibold tabular-nums tracking-tight">{{ fmtGB(overview.traffic) }} <span class="text-base text-mute font-normal">{{ t('gb') }}</span></span>
        <span class="text-xs sm:text-[13px] text-dim">{{ t('ov.trafficNote') }}</span>
      </div>
      <div class="card gap-1.5 sm:gap-2">
        <span class="text-xs sm:text-[13px] text-mute">{{ t('ov.expiring') }}</span>
        <span :class="['text-2xl sm:text-[30px] font-semibold tabular-nums tracking-tight', overview.expiringSoon ? 'text-warn' : '']">{{ overview.expiringSoon }}</span>
        <span class="text-xs sm:text-[13px] text-dim">{{ t('ov.expiringNote') }}</span>
      </div>
    </div>

    <!-- Каскад -->
    <section class="card">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 class="h2">{{ hasExits ? t('ov.cascade') : t('ov.single') }}</h2>
        <span v-if="protoLine()" class="pill bg-acc-bg text-acc">{{ protoLine() }}</span>
      </div>
      <div v-if="!bridges.length && !exits.length" class="text-sm text-mute">
        {{ t('noNodes') }} <button type="button" class="text-acc hover:text-acc-hover" @click="$emit('go', 'nodes')">{{ t('ov.addNode') }}</button>
      </div>
      <div v-else class="flex flex-col md:flex-row md:items-stretch gap-2 md:gap-2.5">
        <div class="md:flex-1 bg-surf2 border border-line rounded-lg px-4 py-3 flex flex-col gap-1">
          <span class="font-semibold text-sm">{{ t('ov.clients') }}</span>
          <span class="text-xs text-mute">{{ t('ov.clientsNote', { n: overview.usersActive }) }}</span>
        </div>
        <div class="flex items-center justify-center text-dim md:rotate-0 rotate-90"><Icon name="arrow" /></div>
        <div class="md:flex-1 flex flex-col gap-2">
          <button v-for="n in bridges" :key="n.IP" type="button" @click="$emit('go', 'nodes')"
            class="flex-1 text-left bg-surf2 border border-line rounded-lg px-4 py-3 flex flex-col gap-1 hover:border-dim transition-colors">
            <span class="flex items-center gap-2 font-semibold text-sm"><span :class="['w-2 h-2 rounded-full', dot(n)]"></span>{{ nodeName(n) }} <span class="text-dim font-normal">· {{ t('nd.roleBridge') }}</span></span>
            <span class="text-xs text-mute font-mono">{{ n.IP }}</span>
          </button>
        </div>
        <template v-if="hasExits">
          <div class="flex items-center justify-center text-dim md:rotate-0 rotate-90"><Icon name="arrow" /></div>
          <div class="md:flex-1 flex flex-col gap-2">
            <button v-for="n in exits" :key="n.IP" type="button" @click="$emit('go', 'nodes')"
              class="flex-1 text-left bg-surf2 border border-line rounded-lg px-4 py-3 flex flex-col gap-1 hover:border-dim transition-colors">
              <span class="flex items-center gap-2 font-semibold text-sm"><span :class="['w-2 h-2 rounded-full', dot(n)]"></span>{{ nodeName(n) }} <span class="text-dim font-normal">· {{ t('nd.roleExit') }}</span></span>
              <span class="text-xs text-mute font-mono">{{ n.IP }}</span>
              <span v-if="linkTo(n.IP)" :class="['text-xs tabular-nums', linkTo(n.IP).loss_pct >= (links?.alert_pct ?? 10) ? 'text-err' : 'text-dim']">
                {{ t('ov.linkNow', { ms: linkTo(n.IP).avg_ms, loss: linkTo(n.IP).loss_pct }) }}</span>
            </button>
          </div>
        </template>
        <div class="flex items-center justify-center text-dim md:rotate-0 rotate-90"><Icon name="arrow" /></div>
        <div class="md:flex-1 bg-surf2 border border-line rounded-lg px-4 py-3 flex flex-col gap-1">
          <span class="font-semibold text-sm">{{ t('ov.internet') }}</span>
          <span class="text-xs text-mute">{{ t('ov.warpRules', { n: routing?.warp_rules?.length || 0 }) }}</span>
        </div>
      </div>
      <div v-if="hasExits && routing" class="flex flex-wrap gap-x-5 gap-y-1 text-[13px] text-mute">
        <span>{{ t('ov.linkLabel') }} <span class="text-fg">{{ routing.exit_link === 'cdn' && routing.cdn ? t('pr.linkCDN') : (routing.exit_link !== 'tcp' && routing.xhttp ? 'XHTTP' : 'TCP') }}</span></span>
        <span>{{ t('ov.failClosed') }}</span>
      </div>
    </section>

    <div class="flex flex-col lg:flex-row gap-4 sm:gap-6 lg:items-start">
      <!-- Требует внимания -->
      <section class="card lg:flex-1 min-w-0">
        <div class="flex items-center justify-between">
          <h2 class="h2">{{ t('ov.attention') }}</h2>
          <span :class="['pill', attention.length ? 'bg-warn-bg text-warn' : 'bg-ok-bg text-ok']">{{ attention.length || '✓' }}</span>
        </div>
        <p v-if="!attention.length" class="text-sm text-mute -mt-2">{{ t('ov.allGood') }}</p>
        <div v-else class="flex flex-col -mt-2">
          <div v-for="(a, i) in attention" :key="i" class="flex gap-3 py-3.5 border-t border-line">
            <Icon name="alert" :class="['mt-0.5', attIcon[a.kind]]" />
            <div class="flex flex-col gap-1 flex-1 min-w-0">
              <span class="text-sm font-medium">{{ a.title }}</span>
              <span class="text-[13px] text-mute leading-relaxed break-words">{{ a.text }}</span>
            </div>
            <button type="button" class="text-[13px] text-acc hover:text-acc-hover whitespace-nowrap self-start"
              @click="a.user ? openUserModal(a.user) : $emit('go', a.view)">{{ a.user ? t('ov.edit') : t('ov.open') }}</button>
          </div>
        </div>
      </section>

      <!-- Трафик -->
      <section class="card lg:w-[400px] shrink-0">
        <div class="flex items-center justify-between">
          <h2 class="h2">{{ t('ov.top') }}</h2>
          <button type="button" class="text-[13px] text-acc hover:text-acc-hover" @click="$emit('go', 'users')">{{ t('ov.allUsers', { n: users.length }) }}</button>
        </div>
        <p v-if="!topUsers.length" class="text-sm text-mute -mt-2">{{ t('ov.noTraffic') }}</p>
        <div v-for="r in topUsers" :key="r.user.ID" class="flex flex-col gap-1.5">
          <div class="flex justify-between gap-3 text-[13px]">
            <span class="truncate">{{ r.user.Name }}</span>
            <span class="text-mute tabular-nums whitespace-nowrap">{{ fmtGB(used(r.user)) }} <span class="text-dim">/ {{ r.limited ? fmtGB(r.user.TrafficQuota) + ' ' + t('gb') : '∞' }}</span></span>
          </div>
          <div class="h-1.5 rounded-full bg-surf2"><div :class="['h-1.5 rounded-full', r.limited && r.pct >= 90 ? 'bg-warn' : 'bg-acc']" :style="{ width: r.pct + '%' }"></div></div>
        </div>
      </section>
    </div>
  </div>
</template>
