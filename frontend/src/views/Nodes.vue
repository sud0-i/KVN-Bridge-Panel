<script setup>
import Icon from '../ui/Icon.vue'
import Seg from '../ui/Seg.vue'
import LinkChart from '../ui/LinkChart.vue'
import MetricChart from '../ui/MetricChart.vue'
import Sheet from '../ui/Sheet.vue'
import Terminal from '../ui/Terminal.vue'
import { ref } from 'vue'
import { t } from '../i18n'
import { nodes, loadingNodes, routing, agentSHA, agentPending, xrayPending, nodeName, ago, fmtDateTime, bridges, exits,
  openDeploy, openRedeploy, editSNI, editLabel, editCDN, deleteNode, links, linksRange, fetchLinks,
  metrics, metricsOf, fetchMetrics, nodeAction, hasPanelKey, terminalFor, setKeysOnly } from '../store'

const emit = defineEmits(['go'])

const errors = (n) => [
  n.ConfigError && { title: t('configError'), text: n.ConfigError },
  n.UpdateError && { title: t('updateError'), text: n.UpdateError },
  n.SNIError && { title: t('sniError'), text: n.SNIError },
  n.WarpError && n.IsOnline && { title: t('warpOff'), text: n.WarpError },
  n.MieruError && routing.value?.mieru && { title: t('nd.mieruError'), text: n.MieruError },
].filter(Boolean)

const protocols = (n) => {
  const r = routing.value
  if (!r) return '—'
  return ['VLESS', r.xhttp && 'XHTTP', r.hysteria && 'Hysteria2', r.mieru && n.MieruVersion && 'mieru', r.cdn && n.CDNDomain && 'CDN'].filter(Boolean).join(' · ')
}
const linkLabel = (n) => {
  const r = routing.value
  if (r.exit_link === 'cdn' && r.cdn && n.CDNDomain) return t('nd.linkCDN', { d: n.CDNDomain })
  if ((r.exit_link === 'xhttp' || r.exit_link === 'cdn') && r.xhttp) return t('nd.linkXHTTP')
  return 'TCP (Vision)'
}
const ranges = [{ value: '24h', label: t('ln.day') }, { value: '7d', label: t('ln.week') }]
const setRange = (v) => { linksRange.value = v; fetchLinks(); fetchMetrics() }

// Показатели сервера: полоски «сейчас» и графики по раскрытию
const open = ref({})
const toggle = (ip) => { open.value = { ...open.value, [ip]: !open.value[ip] } }
const BLUE = '#3987e5'
const ORANGE = '#d95926'
const level = (v, warn, bad) => (v == null ? 'bg-surf2' : v >= bad ? 'bg-err' : v >= warn ? 'bg-warn' : 'bg-acc')
const mbps = (v) => (v == null ? '—' : v >= 100 ? Math.round(v) : v)
const uptime = (sec) => {
  if (!sec) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  return d ? t('mt.upDays', { d, h }) : t('mt.upHours', { h, m: Math.floor((sec % 3600) / 60) })
}

// Действия: перезапуск служб и перезагрузка
const actionsFor = ref(null)
const actionList = (n) => ['restart-xray', ...(n.MieruVersion ? ['restart-mieru'] : []), 'restart-agent', 'reboot']
const runAction = async (n, a) => { actionsFor.value = null; await nodeAction(n, a) }
const byIP = (ip) => nodes.value.find(n => n.IP === ip)
const pendingAgents = () => nodes.value.filter(agentPending).length
</script>

<template>
  <div class="flex flex-col gap-4 sm:gap-6">
    <div v-if="loadingNodes && !nodes.length" class="text-mute">{{ t('loading') }}</div>

    <div class="grid grid-cols-1 md:grid-cols-2 2xl:grid-cols-3 gap-4">
      <section v-for="n in nodes" :key="n.IP" class="card">
        <div class="flex items-start justify-between gap-3">
          <div class="flex flex-col gap-1.5 min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-lg font-semibold truncate">{{ nodeName(n) }}</span>
              <span :class="['pill', n.Type === 'bridge' ? 'bg-acc-bg text-acc' : 'bg-surf2 text-mute']">{{ n.Type === 'bridge' ? t('nd.roleBridge') : t('nd.roleExit') }}</span>
            </div>
            <span class="font-mono text-[13px] text-mute">{{ n.IP }}</span>
          </div>
          <div class="flex flex-wrap gap-1.5 justify-end">
            <span v-if="n.IsOnline" class="pill bg-ok-bg text-ok"><span class="w-2 h-2 rounded-full bg-ok"></span>{{ t('online') }}</span>
            <span v-else class="pill bg-err-bg text-err"><span class="w-2 h-2 rounded-full bg-err"></span>{{ t('offline') }}</span>
            <span v-if="n.IsOnline && (n.Type === 'exit' || n.WarpOK || n.WarpError)" :class="['pill', n.WarpOK ? 'bg-ok-bg text-ok' : 'bg-surf2 text-mute']">WARP</span>
          </div>
        </div>

        <div v-for="(e, i) in errors(n)" :key="i" class="bg-err-bg rounded-lg px-3.5 py-3 flex gap-2.5 text-err text-[13px] leading-relaxed">
          <Icon name="alert" :size="16" class="mt-0.5" />
          <span class="min-w-0 break-words"><b class="font-semibold">{{ e.title }}.</b> <span class="text-err-soft">{{ e.text }}</span></span>
        </div>

        <!-- Показатели сервера -->
        <div v-if="metricsOf(n.IP)?.current" class="flex flex-col gap-2.5">
          <div class="grid grid-cols-3 gap-3 text-xs">
            <div v-for="b in [
              { k: 'cpu', label: t('mt.cpu'), v: metricsOf(n.IP).current.cpu, w: 70, e: 90 },
              { k: 'mem', label: t('mt.mem'), v: metricsOf(n.IP).current.mem, w: 80, e: 95 },
              { k: 'disk', label: t('mt.disk'), v: metricsOf(n.IP).current.disk, w: 80, e: 90 }]" :key="b.k" class="flex flex-col gap-1">
              <div class="flex justify-between gap-1"><span class="text-mute">{{ b.label }}</span><span class="tabular-nums">{{ b.v == null ? '—' : Math.round(b.v) + '%' }}</span></div>
              <div class="h-1.5 rounded-full bg-surf2"><div :class="['h-1.5 rounded-full', level(b.v, b.w, b.e)]" :style="{ width: (b.v || 0) + '%' }"></div></div>
            </div>
          </div>
          <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-mute">
            <span>{{ t('mt.net') }} <span class="text-fg tabular-nums">↓{{ mbps(metricsOf(n.IP).current.rx_mbps) }} ↑{{ mbps(metricsOf(n.IP).current.tx_mbps) }}</span> {{ t('mt.mbit') }}</span>
            <span>steal <span :class="['tabular-nums', (metricsOf(n.IP).current.steal || 0) >= (metrics?.alerts?.steal ?? 20) ? 'text-err' : 'text-fg']">{{ metricsOf(n.IP).current.steal ?? '—' }}%</span></span>
            <span v-if="metricsOf(n.IP).current.retrans != null">{{ t('mt.retrans') }} <span :class="['tabular-nums', metricsOf(n.IP).current.retrans >= (metrics?.alerts?.retrans ?? 5) ? 'text-err' : 'text-fg']">{{ metricsOf(n.IP).current.retrans }}%</span></span>
            <span>TCP <span class="text-fg tabular-nums">{{ metricsOf(n.IP).current.conns }}</span></span>
            <span>{{ t('mt.uptime') }} <span class="text-fg">{{ uptime(metricsOf(n.IP).last?.UptimeSec) }}</span></span>
          </div>
          <button type="button" class="self-start text-[13px] text-acc hover:text-acc-hover flex items-center gap-1" :aria-expanded="!!open[n.IP]" @click="toggle(n.IP)">
            <Icon :name="open[n.IP] ? 'up' : 'chevron'" :size="14" />{{ open[n.IP] ? t('mt.hide') : t('mt.charts') }}</button>
          <div v-if="open[n.IP]" class="flex flex-col gap-4 border-t border-line pt-3">
            <Seg :model-value="linksRange" @update:model-value="setRange" :options="ranges" :label="t('mt.charts')" class="self-start" />
            <MetricChart :title="t('mt.cpuChart')" :points="metricsOf(n.IP).points" :since="metrics.since" :step-minutes="metrics.step_minutes" :max="100"
              :series="[{ key: 'cpu', label: t('mt.cpu'), color: BLUE }, { key: 'steal', label: 'steal', color: ORANGE }]" />
            <MetricChart :title="t('mt.memChart')" :points="metricsOf(n.IP).points" :since="metrics.since" :step-minutes="metrics.step_minutes" :max="100"
              :series="[{ key: 'mem', label: t('mt.mem'), color: BLUE }]" />
            <MetricChart :title="t('mt.netChart')" :points="metricsOf(n.IP).points" :since="metrics.since" :step-minutes="metrics.step_minutes" :unit="t('mt.mbit')"
              :series="[{ key: 'rx_mbps', label: t('mt.rx'), color: BLUE }, { key: 'tx_mbps', label: t('mt.tx'), color: ORANGE }]" />
            <MetricChart :title="t('mt.retransChart')" :points="metricsOf(n.IP).points" :since="metrics.since" :step-minutes="metrics.step_minutes" :threshold="metrics.alerts?.retrans || 5"
              :series="[{ key: 'retrans', label: t('mt.retrans'), color: BLUE }]" />
          </div>
        </div>

        <dl class="flex flex-col text-[13px]">
          <div class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('nd.sni') }}</dt>
            <dd class="text-right break-all">{{ n.RealityDest ? t('nd.ownSite', { d: n.Domain || n.SNI }) : (n.SNI || '—') }}</dd>
          </div>
          <div class="flex justify-between items-center gap-4 py-2 border-t border-line">
            <dt class="text-mute">CDN</dt>
            <dd class="text-right flex items-center gap-2 min-w-0">
              <span :class="['break-all', n.CDNDomain ? (routing?.cdn ? '' : 'text-dim') : 'text-dim']" :title="n.CDNDomain && !routing?.cdn ? t('nd.cdnOff') : ''">{{ n.CDNDomain || '—' }}</span>
              <button type="button" class="text-dim hover:text-acc" :aria-label="t('nd.cdnEdit')" :title="t('nd.cdnEdit')" @click="editCDN(n)"><Icon name="edit" :size="14" /></button>
            </dd>
          </div>
          <div class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('nd.protocols') }}</dt><dd class="text-right">{{ protocols(n) }}</dd>
          </div>
          <div v-if="n.Type === 'bridge' && exits.length" class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('nd.exits') }}</dt><dd class="text-right">{{ exits.map(nodeName).join(', ') }}</dd>
          </div>
          <div v-if="n.Type === 'exit' && routing" class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('nd.link') }}</dt><dd class="text-right">{{ linkLabel(n) }}</dd>
          </div>
          <div class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">Xray</dt>
            <dd class="text-right font-mono">{{ n.XrayVersion || '—' }}<span v-if="xrayPending(n)" class="text-warn" :title="t('xrayPending')"> → {{ routing.xray_version }}</span></dd>
          </div>
          <div class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('nd.agent') }}</dt>
            <dd class="text-right font-mono" :title="n.AgentVersion">
              <span :class="agentPending(n) ? 'text-warn' : ''">{{ n.AgentVersion ? n.AgentVersion.slice(0, 7) : '—' }}</span>
              <span v-if="agentPending(n)" class="text-warn"> → {{ agentSHA.slice(0, 7) }}</span>
            </dd>
          </div>
          <div class="flex justify-between items-center gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('tm.ssh') }}</dt>
            <dd class="text-right flex items-center gap-2 justify-end">
              <span v-if="n.SSHState?.startsWith('error')" class="text-err text-[13px]" :title="n.SSHState">{{ n.SSHState.slice(7) }}</span>
              <span v-else-if="!hasPanelKey(n)" class="text-dim text-[13px]" :title="t('tm.sshNoneHint')">{{ t('tm.sshNone') }}</span>
              <template v-else>
                <span :class="['text-[13px]', n.SSHKeysOnly ? 'text-ok' : '']">{{ n.SSHKeysOnly ? t('tm.sshKeysOnly') : t('tm.sshKeys') }}</span>
                <button type="button" class="text-[13px] text-acc hover:text-acc-hover" @click="setKeysOnly(n, !n.SSHKeysOnly)">{{ n.SSHKeysOnly ? t('tm.keysOnlyOff') : t('tm.keysOnlyOn') }}</button>
              </template>
            </dd>
          </div>
          <div class="flex justify-between gap-4 py-2 border-t border-line">
            <dt class="text-mute">{{ t('nd.geo') }}</dt><dd class="text-right font-mono">{{ n.GeoUpdated ? fmtDateTime(n.GeoUpdated) : '—' }}</dd>
          </div>
        </dl>

        <div class="flex items-center gap-2 mt-auto">
          <span class="text-xs text-dim flex-1 min-w-0">{{ n.RealityDest ? t('nd.onMaster') + ' · ' : '' }}{{ t('nd.synced', { ago: ago(n.LastSeen) }) }}
            <template v-if="n.PendingAction"> · <span class="text-warn">{{ t('act.pending', { a: t('act.name.' + n.PendingAction) }) }}</span></template>
            <template v-else-if="n.ActionResult"> · <span :class="n.ActionResult.includes(': ok') ? 'text-ok' : n.ActionResult.includes('ошибка') || n.ActionResult.includes('отменено') ? 'text-err' : ''">{{ n.ActionResult }}</span></template></span>
          <button v-if="hasPanelKey(n)" type="button" class="icon-btn" :aria-label="t('tm.open')" :title="t('tm.open')" @click="terminalFor = n"><Icon name="terminal" :size="16" /></button>
          <button v-if="n.IsOnline" type="button" class="icon-btn" :aria-label="t('act.title')" :title="t('act.title')" @click="actionsFor = n"><Icon name="power" :size="16" /></button>
          <button type="button" class="icon-btn" :aria-label="t('labelHint')" :title="t('labelHint')" @click="editLabel(n)"><Icon name="tag" :size="16" /></button>
          <button v-if="!n.RealityDest" type="button" class="icon-btn" :aria-label="t('editSNI')" :title="t('editSNI')" @click="editSNI(n)"><Icon name="edit" :size="16" /></button>
          <button v-if="!n.RealityDest" type="button" class="icon-btn" :aria-label="t('redeploy')" :title="t('redeploy')" @click="openRedeploy(n.IP)"><Icon name="refresh" :size="16" /></button>
          <button type="button" class="icon-btn text-err hover:text-err" :aria-label="t('delete')" :title="t('delete')" @click="deleteNode(n)"><Icon name="trash" :size="16" /></button>
        </div>
      </section>

      <button type="button" @click="openDeploy"
        class="min-h-[180px] border border-dashed border-line rounded-xl text-mute flex flex-col items-center justify-center gap-2.5 p-6 hover:border-dim transition-colors">
        <span class="w-11 h-11 rounded-full bg-surf2 flex items-center justify-center text-acc"><Icon name="plus" :size="20" /></span>
        <span class="text-[15px] text-fg font-medium">{{ t('nd.add') }}</span>
        <span class="text-[13px] max-w-[260px] text-center leading-relaxed">{{ t('nd.addHint') }}</span>
      </button>
    </div>

    <!-- Канал мост → выходные ноды -->
    <section v-if="exits.length" class="card">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex flex-col gap-1">
          <h2 class="h2">{{ t('ln.title') }}</h2>
          <p class="text-[13px] text-mute leading-relaxed">{{ t('ln.hint') }}</p>
        </div>
        <Seg :model-value="linksRange" @update:model-value="setRange" :options="ranges" :label="t('ln.title')" />
      </div>
      <p v-if="links && !links.links.length" class="text-sm text-dim">{{ t('ln.empty') }}</p>
      <div v-for="l in links?.links || []" :key="l.from + l.to" class="flex flex-col gap-3 border-t border-line pt-4 first-of-type:border-0 first-of-type:pt-0">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <span class="text-sm font-medium">{{ nodeName(byIP(l.from) || { IP: l.from }) }} → {{ nodeName(byIP(l.to) || { IP: l.to }) }}</span>
          <span v-if="l.current" class="text-[13px] text-mute">{{ t('ln.now') }}:
            <span class="text-fg tabular-nums">{{ l.current.avg_ms }} {{ t('ln.ms') }}</span> ·
            <span :class="['tabular-nums', l.current.loss_pct >= links.alert_pct ? 'text-err' : 'text-fg']">{{ t('ln.lossN', { n: l.current.loss_pct }) }}</span></span>
        </div>
        <LinkChart :points="l.points" :since="links.since" :step-minutes="links.step_minutes" :alert-pct="links.alert_pct" />
      </div>
    </section>

    <section v-if="nodes.length" class="card">
      <div class="flex items-center justify-between gap-3">
        <h2 class="h2">{{ t('updates') }}</h2>
        <button type="button" class="text-[13px] text-acc hover:text-acc-hover" @click="$emit('go', 'settings')">{{ t('nd.updateSettings') }}</button>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-4 text-[13px]">
        <div class="flex flex-col gap-1">
          <span class="text-mute">{{ t('xrayVersion') }}</span>
          <span class="font-mono text-[15px]">{{ routing?.xray_version || t('nd.notManaged') }}</span>
        </div>
        <div class="flex flex-col gap-1">
          <span class="text-mute">{{ t('nd.agent') }}</span>
          <span class="font-mono text-[15px]">{{ agentSHA ? agentSHA.slice(0, 7) : '—' }}
            <span v-if="pendingAgents()" class="text-warn font-sans text-[13px]">· {{ t('nd.pending', { n: pendingAgents() }) }}</span></span>
        </div>
        <div class="flex flex-col gap-1">
          <span class="text-mute">{{ t('nd.geo') }}</span>
          <span class="text-[15px]">{{ routing?.geo_update ? t('nd.geoNightly') : t('nd.geoOff') }}</span>
        </div>
      </div>
    </section>
  </div>
  <Terminal v-if="terminalFor" :node="terminalFor" @close="terminalFor = null" @go="(v) => emit('go', v)" />
  <Sheet v-if="actionsFor" :title="t('act.title') + ': ' + nodeName(actionsFor)" @close="actionsFor = null">
    <p class="text-[13px] text-mute leading-relaxed">{{ t('act.hint') }}</p>
    <div class="flex flex-col -mx-2">
      <button v-for="a in actionList(actionsFor)" :key="a" type="button" @click="runAction(actionsFor, a)"
        :class="['flex flex-col items-start gap-0.5 px-3 py-2.5 rounded-lg text-left hover:bg-surf2', a === 'reboot' ? 'text-err' : '']">
        <span class="text-[15px]">{{ t('act.name.' + a) }}</span>
        <span class="text-xs text-dim">{{ t('act.desc.' + a) }}</span>
      </button>
    </div>
  </Sheet>
</template>
