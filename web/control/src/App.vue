<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useStorage } from '@vueuse/core'
import {
  Bot,
  Check,
  Database,
  DoorOpen,
  KeyRound,
  Loader2,
  MessageCircle,
  MessageSquarePlus,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
} from 'lucide-vue-next'
import {
  api,
  type Agent,
  type Credentials,
  type Delivery,
  type InjectMessageInput,
  type MemoryItem,
  type Message,
  type RegisterRoomInput,
  type RoomSummary,
  type Timeline,
  type UpsertAgentInput,
} from './api'

type Tab = 'timeline' | 'memory' | 'agents' | 'settings'

const savedCredentials = useStorage<Credentials>('tinyclaw.control.credentials', {
  clientId: 'admin',
  clientSecret: '',
})

const clientId = ref(savedCredentials.value.clientId)
const clientSecret = ref(savedCredentials.value.clientSecret)
const rememberCredentials = useStorage('tinyclaw.control.remember_credentials', true)
const editingCredentials = ref(false)
const chatUserId = useStorage('tinyclaw.control.chat_user_id', 'guest')
const rooms = ref<RoomSummary[]>([])
const agents = ref<Agent[]>([])
const selectedRoomId = useStorage<number | null>('tinyclaw.control.selected_room_id', null)
const selectedAgentId = useStorage<number | null>('tinyclaw.control.selected_agent_id', null)
const timeline = ref<Timeline | null>(null)
const memoryItems = ref<MemoryItem[]>([])
const activeTab = ref<Tab>('timeline')
const query = ref('')
const agentQuery = ref('')
const roomLoading = ref(false)
const detailLoading = ref(false)
const memoryLoading = ref(false)
const savingRoom = ref(false)
const agentLoading = ref(false)
const savingAgent = ref(false)
const authError = ref('')
const actionError = ref('')
const injectText = ref('')
const injecting = ref(false)
const ackingDeliveryId = ref<number | null>(null)
const memoryStatus = ref('active')
const memoryTypes = ref<string[]>([])
const settingsChannel = ref('')
const settingsChannelRoomId = ref('')
const settingsChannelRoomType = ref('group')
const settingsDisplayName = ref('')
const settingsOutboundAlias = ref('')
const settingsAgentEnabled = ref(true)
const settingsTriggerPolicy = ref('')
const agentKey = ref('')
const agentDisplayName = ref('')
const agentDescription = ref('')
const agentOwnerId = ref('')
const agentShared = ref(false)
const agentPrompt = ref('')
const agentAllowedTools = ref('[]')
const agentEnabled = ref(true)

const credentials = computed<Credentials>(() => ({
  clientId: clientId.value.trim(),
  clientSecret: clientSecret.value,
}))

const selectedRoom = computed(() =>
  rooms.value.find((summary) => summary.room.id === selectedRoomId.value) ?? null,
)

const selectedAgent = computed(() => agents.value.find((agent) => agent.id === selectedAgentId.value) ?? null)

const filteredRooms = computed(() => {
  const value = query.value.trim().toLowerCase()
  if (!value) return rooms.value
  return rooms.value.filter(({ room }) =>
    [
      room.display_name,
      room.channel,
      room.channel_room_id,
      room.channel_room_type,
      room.outbound_alias,
    ]
      .filter(Boolean)
      .some((part) => String(part).toLowerCase().includes(value)),
  )
})

const filteredAgents = computed(() => {
  const value = agentQuery.value.trim().toLowerCase()
  if (!value) return agents.value
  return agents.value.filter((agent) =>
    [agent.key, agent.display_name, agent.description, agent.owner_id, agent.visibility]
      .filter(Boolean)
      .some((part) => String(part).toLowerCase().includes(value)),
  )
})

const sortedMessages = computed(() =>
  [...(timeline.value?.messages ?? [])].sort((a, b) => a.id - b.id),
)

const deliveryRows = computed(() =>
  [...(timeline.value?.deliveries ?? [])].sort((a, b) => a.id - b.id),
)

const pendingDeliveries = computed(() => deliveryRows.value.filter((delivery) => delivery.status === 0))

const messageDeliveries = computed(() => {
  const grouped = new Map<number, Delivery[]>()
  for (const delivery of deliveryRows.value) {
    const current = grouped.get(delivery.source_message_to_id) ?? []
    current.push(delivery)
    grouped.set(delivery.source_message_to_id, current)
  }
  return grouped
})

const canUseAPI = computed(() => credentials.value.clientId.length > 0 && credentials.value.clientSecret.length > 0)
const showCredentialForm = computed(() => !canUseAPI.value || authError.value !== '' || editingCredentials.value)

const canShowWorkspace = computed(() => selectedRoom.value !== null || activeTab.value === 'agents' || activeTab.value === 'settings')

const formatDate = (value?: string) => {
  if (!value || value.startsWith('0001-')) return 'Never'
  return new Intl.DateTimeFormat(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

const formatMessageTime = (message: Message) => {
  if (message.msgtime > 0) return formatDate(new Date(message.msgtime * 1000).toISOString())
  return formatDate(message.created_at)
}

const textFromBody = (body: Record<string, unknown>) => {
  const content = body.content
  if (typeof content === 'string' && content.trim()) return content
  const textObject = body.text
  if (textObject && typeof textObject === 'object' && 'content' in textObject) {
    const textContent = (textObject as { content?: unknown }).content
    if (typeof textContent === 'string' && textContent.trim()) return textContent
  }
  const text = body.text
  if (typeof text === 'string' && text.trim()) return text
  return JSON.stringify(body)
}

const textFromDelivery = (delivery: Delivery) => {
  const text = delivery.payload.text
  if (typeof text === 'string' && text.trim()) return text
  const content = delivery.payload.content
  if (typeof content === 'string' && content.trim()) return content
  return JSON.stringify(delivery.payload)
}

const isSharedAgent = (agent: Agent) => agent.visibility !== 'private'

const isOutboundMessage = (message: Message) =>
  message.from === chatUserId.value || ['admin', 'agent'].includes(message.source)

const deliveryStatusLabel = (status: number) => {
  if (status === 0) return 'Pending'
  if (status === 1) return 'Acked'
  if (status === 2) return 'Failed'
  return `Status ${status}`
}

const syncSettingsFromSelectedRoom = () => {
  const summary = selectedRoom.value
  if (!summary) return
  settingsChannel.value = summary.room.channel
  settingsChannelRoomId.value = summary.room.channel_room_id
  settingsChannelRoomType.value = summary.room.channel_room_type
  settingsDisplayName.value = summary.room.display_name ?? ''
  settingsOutboundAlias.value = summary.room.outbound_alias
  settingsAgentEnabled.value = summary.agent_session?.enabled ?? false
  settingsTriggerPolicy.value = summary.agent_session?.trigger_policy
    ? JSON.stringify(summary.agent_session.trigger_policy, null, 2)
    : ''
}

const resetRoomForm = () => {
  settingsChannel.value = 'wecom'
  settingsChannelRoomId.value = ''
  settingsChannelRoomType.value = 'group'
  settingsDisplayName.value = ''
  settingsOutboundAlias.value = ''
  settingsAgentEnabled.value = true
  settingsTriggerPolicy.value = ''
}

const loadRooms = async () => {
  if (!canUseAPI.value) return
  roomLoading.value = true
  authError.value = ''
  try {
    const result = await api.listRooms(credentials.value)
    rooms.value = result.rooms ?? []
    if (!rooms.value.some((summary) => summary.room.id === selectedRoomId.value)) {
      selectedRoomId.value = rooms.value[0]?.room.id ?? null
      timeline.value = null
      memoryItems.value = []
    }
  } catch (error) {
    authError.value = error instanceof Error ? error.message : 'Failed to load rooms'
  } finally {
    roomLoading.value = false
  }
}

const loadAgents = async () => {
  if (!canUseAPI.value) return
  agentLoading.value = true
  actionError.value = ''
  try {
    const result = await api.listAgents(credentials.value)
    agents.value = result.agents ?? []
    if (!agents.value.some((agent) => agent.id === selectedAgentId.value)) {
      selectedAgentId.value = agents.value[0]?.id ?? null
    }
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to load agents'
  } finally {
    agentLoading.value = false
  }
}

const loadTimeline = async () => {
  if (!canUseAPI.value || !selectedRoomId.value) return
  detailLoading.value = true
  actionError.value = ''
  try {
    timeline.value = await api.getTimeline(credentials.value, selectedRoomId.value)
    timeline.value.messages ??= []
    timeline.value.deliveries ??= []
    timeline.value.agent_sessions ??= []
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to load timeline'
  } finally {
    detailLoading.value = false
  }
}

const loadMemory = async () => {
  if (!canUseAPI.value || !selectedRoomId.value) return
  memoryLoading.value = true
  actionError.value = ''
  try {
    const result = await api.listMemory(credentials.value, selectedRoomId.value, memoryStatus.value, memoryTypes.value)
    memoryItems.value = result.items ?? []
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to load memory'
  } finally {
    memoryLoading.value = false
  }
}

const selectRoom = async (roomId: number) => {
  selectedRoomId.value = roomId
  activeTab.value = 'timeline'
  await loadTimeline()
}

const newRoom = () => {
  selectedRoomId.value = null
  timeline.value = null
  memoryItems.value = []
  activeTab.value = 'settings'
  resetRoomForm()
}

const openAgents = async () => {
  activeTab.value = 'agents'
  await loadAgents()
}

const openRooms = async () => {
  activeTab.value = selectedRoom.value ? 'timeline' : 'settings'
  if (!selectedRoomId.value && rooms.value[0]) {
    selectedRoomId.value = rooms.value[0].room.id
    activeTab.value = 'timeline'
  }
  if (selectedRoomId.value) await loadTimeline()
}

const refreshCurrent = async () => {
  await loadRooms()
  await loadAgents()
  await loadTimeline()
  if (activeTab.value === 'memory') await loadMemory()
}

const saveCredentials = async () => {
  if (rememberCredentials.value) {
    savedCredentials.value = credentials.value
  } else {
    savedCredentials.value = { clientId: 'admin', clientSecret: '' }
  }
  await refreshCurrent()
  if (!authError.value) editingCredentials.value = false
}

const injectMessage = async () => {
  if (!selectedRoomId.value || !injectText.value.trim()) return
  injecting.value = true
  actionError.value = ''
  try {
    const input: InjectMessageInput = {
      sender_id: chatUserId.value.trim() || 'guest',
      text: injectText.value.trim(),
      suppress_agent_trigger: false,
    }
    await api.injectMessage(credentials.value, selectedRoomId.value, input)
    injectText.value = ''
    await Promise.all([loadRooms(), loadTimeline()])
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to inject message'
  } finally {
    injecting.value = false
  }
}

const ackDelivery = async (deliveryId: number) => {
  ackingDeliveryId.value = deliveryId
  actionError.value = ''
  try {
    await api.ackDelivery(credentials.value, deliveryId)
    await Promise.all([loadRooms(), loadTimeline()])
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to ack delivery'
  } finally {
    ackingDeliveryId.value = null
  }
}

const saveRoomSettings = async () => {
  savingRoom.value = true
  actionError.value = ''
  try {
    let triggerPolicy: unknown
    if (settingsTriggerPolicy.value.trim()) {
      triggerPolicy = JSON.parse(settingsTriggerPolicy.value)
    }
    const input: RegisterRoomInput = {
      channel: settingsChannel.value.trim(),
      channel_room_id: settingsChannelRoomId.value.trim(),
      channel_room_type: settingsChannelRoomType.value.trim(),
      display_name: settingsDisplayName.value.trim(),
      outbound_alias: settingsOutboundAlias.value.trim() || settingsChannelRoomId.value.trim(),
      agent_enabled: settingsAgentEnabled.value,
      trigger_policy: triggerPolicy,
    }
    const result = await api.registerRoom(credentials.value, input)
    selectedRoomId.value = result.room.id
    activeTab.value = 'timeline'
    await Promise.all([loadRooms(), loadTimeline()])
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to save room settings'
  } finally {
    savingRoom.value = false
  }
}

const syncAgentForm = () => {
  const agent = selectedAgent.value
  if (!agent) {
    agentKey.value = ''
    agentDisplayName.value = ''
    agentDescription.value = ''
    agentOwnerId.value = chatUserId.value.trim() || 'guest'
    agentShared.value = false
    agentPrompt.value = ''
    agentAllowedTools.value = '[]'
    agentEnabled.value = true
    return
  }
  agentKey.value = agent.key
  agentDisplayName.value = agent.display_name
  agentDescription.value = agent.description ?? ''
  agentOwnerId.value = agent.owner_id || chatUserId.value.trim() || 'guest'
  agentShared.value = isSharedAgent(agent)
  agentPrompt.value = agent.prompt
  agentAllowedTools.value = JSON.stringify(agent.allowed_tools ?? [], null, 2)
  agentEnabled.value = agent.enabled
}

const newAgent = () => {
  selectedAgentId.value = null
  syncAgentForm()
}

const selectAgent = (agentId: number) => {
  selectedAgentId.value = agentId
  syncAgentForm()
}

const saveAgent = async () => {
  savingAgent.value = true
  actionError.value = ''
  try {
    const input: UpsertAgentInput = {
      key: agentKey.value.trim(),
      display_name: agentDisplayName.value.trim(),
      description: agentDescription.value.trim(),
      owner_id: agentOwnerId.value.trim() || chatUserId.value.trim() || 'guest',
      visibility: agentShared.value ? 'shared' : 'private',
      prompt: agentPrompt.value.trim(),
      allowed_tools: agentAllowedTools.value.trim() ? JSON.parse(agentAllowedTools.value) : [],
      enabled: agentEnabled.value,
    }
    const result = selectedAgentId.value
      ? await api.updateAgent(credentials.value, selectedAgentId.value, input)
      : await api.createAgent(credentials.value, input)
    selectedAgentId.value = result.agent.id
    await loadAgents()
    syncAgentForm()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : 'Failed to save agent'
  } finally {
    savingAgent.value = false
  }
}

watch(activeTab, (tab) => {
  if (tab === 'memory') void loadMemory()
  if (tab === 'agents') void loadAgents()
})

watch(memoryStatus, () => {
  if (activeTab.value === 'memory') void loadMemory()
})

watch(selectedRoom, syncSettingsFromSelectedRoom, { immediate: true })
watch(selectedAgent, syncAgentForm, { immediate: true })

onMounted(async () => {
  if (canUseAPI.value) {
    await loadRooms()
    await loadAgents()
    await loadTimeline()
  }
})
</script>

<template>
  <main class="min-h-screen bg-panel text-ink">
    <section v-if="showCredentialForm" class="auth-overlay">
      <form class="auth-form grid grid-cols-[minmax(120px,180px)_minmax(180px,1fr)_auto_auto] items-end gap-3" @submit.prevent="saveCredentials">
        <label class="grid gap-1 text-xs text-muted">
          Client ID
          <input v-model="clientId" class="field" autocomplete="username" />
        </label>
        <label class="grid gap-1 text-xs text-muted">
          Client Secret
          <input v-model="clientSecret" class="field" type="password" autocomplete="current-password" />
        </label>
        <label class="mb-2 flex items-center gap-2 text-sm text-muted">
          <input v-model="rememberCredentials" type="checkbox" />
          Remember
        </label>
        <button class="primary-button" :disabled="roomLoading">
          <KeyRound class="h-4 w-4" />
          Connect
        </button>
      </form>
      <p v-if="authError" class="mt-2 text-sm text-danger">{{ authError }}</p>
    </section>

    <section class="app-shell grid min-h-screen grid-cols-[64px_340px_minmax(0,1fr)]">
      <nav class="global-nav border-r border-line bg-[#e9eef3]" aria-label="Primary">
        <div class="global-brand" title="TinyClaw Chat">
          <ShieldCheck class="h-5 w-5" />
        </div>
        <button
          class="global-nav-button"
          :class="{ 'global-nav-button-active': activeTab !== 'agents' }"
          type="button"
          title="Channels"
          :disabled="!canUseAPI"
          @click="openRooms"
        >
          <MessageCircle class="h-5 w-5" />
          <span>Channels</span>
        </button>
        <div class="global-nav-spacer"></div>
        <button
          class="global-nav-button"
          :class="{ 'global-nav-button-active': activeTab === 'agents' }"
          type="button"
          title="Agent config"
          :disabled="!canUseAPI"
          @click="openAgents"
        >
          <Bot class="h-5 w-5" />
          <span>Agent config</span>
        </button>
        <button
          class="global-nav-button"
          type="button"
          title="Identity and API"
          @click="editingCredentials = true"
        >
          <KeyRound class="h-5 w-5" />
          <span>{{ chatUserId || credentials.clientId || 'Account' }}</span>
        </button>
        <button class="global-nav-button" title="Refresh" :disabled="roomLoading || detailLoading" @click="refreshCurrent">
          <Loader2 v-if="roomLoading || detailLoading" class="h-5 w-5 animate-spin" />
          <RefreshCw v-else class="h-5 w-5" />
          <span>Refresh</span>
        </button>
      </nav>

      <aside class="min-h-0 border-r border-line bg-surface">
        <template v-if="activeTab === 'agents'">
          <div class="border-b border-line p-4">
            <div class="mb-3 flex items-center justify-between">
              <h2 class="text-sm font-700">Agents</h2>
              <div class="flex items-center gap-2">
                <span class="text-xs text-muted">{{ filteredAgents.length }} / {{ agents.length }}</span>
                <button class="icon-button" title="New agent" @click="newAgent">
                  <Plus class="h-4 w-4" />
                </button>
              </div>
            </div>
            <label class="flex items-center gap-2 rounded-6px border border-line bg-white px-3 py-2">
              <Search class="h-4 w-4 text-muted" />
              <input v-model="agentQuery" class="min-w-0 flex-1 bg-transparent text-sm outline-none" placeholder="Search agents" />
            </label>
          </div>

          <div class="room-list h-[calc(100vh-173px)] overflow-y-auto">
            <div v-if="agentLoading" class="empty-state m-4">
              <Loader2 class="h-4 w-4 animate-spin" />
              Loading agents
            </div>
            <button
              v-for="agent in filteredAgents"
              v-else
              :key="agent.id"
              class="room-row"
              :class="{ 'room-row-active': agent.id === selectedAgentId }"
              @click="selectAgent(agent.id)"
            >
              <span class="min-w-0">
                <span class="block truncate text-sm font-650">{{ agent.display_name }}</span>
                <span class="mt-1 block truncate text-xs text-muted">@{{ agent.key }} · {{ agent.owner_id || 'system' }}</span>
              </span>
              <span class="grid justify-items-end gap-1">
                <span class="badge-muted">{{ isSharedAgent(agent) ? 'Shared' : 'Private' }}</span>
                <span class="text-xs text-muted">{{ agent.enabled ? 'Enabled' : 'Disabled' }}</span>
              </span>
            </button>
            <div v-if="!agentLoading && !filteredAgents.length" class="p-4 text-sm text-muted">
              No agents loaded.
            </div>
          </div>
        </template>

        <template v-else>
          <div class="border-b border-line p-4">
            <div class="mb-3 flex items-center justify-between">
              <h2 class="text-sm font-700">Channels</h2>
              <div class="flex items-center gap-2">
                <span class="text-xs text-muted">{{ filteredRooms.length }} / {{ rooms.length }}</span>
                <button class="icon-button" title="Join channel" @click="newRoom">
                  <Plus class="h-4 w-4" />
                </button>
              </div>
            </div>
            <label class="flex items-center gap-2 rounded-6px border border-line bg-white px-3 py-2">
              <Search class="h-4 w-4 text-muted" />
              <input v-model="query" class="min-w-0 flex-1 bg-transparent text-sm outline-none" placeholder="Search channels" />
            </label>
            <label class="mt-3 grid gap-1 text-xs text-muted">
              Chat identity
              <input v-model="chatUserId" class="field text-sm" placeholder="guest" />
            </label>
          </div>

          <div class="room-list h-[calc(100vh-173px)] overflow-y-auto">
            <button
              v-for="summary in filteredRooms"
              :key="summary.room.id"
              class="room-row"
              :class="{ 'room-row-active': summary.room.id === selectedRoomId }"
              @click="selectRoom(summary.room.id)"
            >
              <span class="min-w-0">
                <span class="block truncate text-sm font-650">
                  {{ summary.room.display_name || summary.room.channel_room_id }}
                </span>
                <span class="mt-1 block truncate text-xs text-muted">
                  #{{ summary.room.channel_room_id }} · {{ summary.room.channel }} · {{ summary.room.channel_room_type }}
                </span>
              </span>
              <span class="grid justify-items-end gap-1">
                <span v-if="summary.room.id === selectedRoomId" class="badge-muted">Joined</span>
                <span v-if="summary.pending_delivery_count" class="badge-warn">{{ summary.pending_delivery_count }}</span>
                <span class="text-xs text-muted">{{ formatDate(summary.last_message_time) }}</span>
              </span>
            </button>

            <div v-if="!filteredRooms.length" class="p-4 text-sm text-muted">
              No channels available.
            </div>
          </div>
        </template>
      </aside>

      <section class="min-w-0 min-h-0">
        <div v-if="canShowWorkspace" class="workspace-body grid h-full grid-rows-[auto_auto_minmax(0,1fr)]">
          <div class="workspace-header border-b border-line bg-surface px-5 py-4">
            <div class="workspace-heading flex items-start justify-between gap-4">
              <div class="min-w-0">
                <h2 class="workspace-title truncate text-xl font-750">
                  <template v-if="activeTab === 'agents'">Agent Config</template>
                  <template v-else-if="selectedRoom">
                    {{ selectedRoom.room.display_name || selectedRoom.room.channel_room_id }}
                  </template>
                  <template v-else>Join Channel</template>
                </h2>
                <p v-if="activeTab === 'agents'" class="workspace-subtitle mt-1 text-sm text-muted">
                  Configure bot definitions used by channels.
                </p>
                <p v-else-if="selectedRoom" class="workspace-subtitle mt-1 text-sm text-muted">
                  Joined as {{ chatUserId || 'guest' }} · #{{ selectedRoom.room.channel_room_id }} · {{ selectedRoom.room.channel }}
                </p>
                <p v-else class="workspace-subtitle mt-1 text-sm text-muted">
                  Join an existing channel or register a new one.
                </p>
              </div>
              <div v-if="selectedRoom && activeTab !== 'agents'" class="workspace-status flex gap-2 text-xs">
                <span class="status-pill">{{ selectedRoom.agent_session?.enabled ? 'Bot online' : 'Bot muted' }}</span>
                <span class="status-pill">{{ pendingDeliveries.length }} pending</span>
              </div>
            </div>
          </div>

          <div v-if="activeTab !== 'agents'" class="border-b border-line bg-surface px-5">
            <nav class="workspace-tabs flex gap-1">
              <button
                class="tab-button"
                :class="{ 'tab-button-active': activeTab === 'timeline' }"
                :disabled="!selectedRoom"
                @click="activeTab = 'timeline'"
              >
                Chat
              </button>
              <button
                class="tab-button"
                :class="{ 'tab-button-active': activeTab === 'memory' }"
                :disabled="!selectedRoom"
                @click="activeTab = 'memory'"
              >
                Memory
              </button>
              <button class="tab-button" :class="{ 'tab-button-active': activeTab === 'settings' }" @click="activeTab = 'settings'">
                Channel settings
              </button>
            </nav>
          </div>

          <div class="min-h-0 overflow-y-auto p-5">
            <p v-if="actionError" class="mb-4 rounded-6px border border-danger/25 bg-white px-3 py-2 text-sm text-danger">
              {{ actionError }}
            </p>

            <section v-if="activeTab === 'timeline'" class="chat-layout">
              <div class="chat-thread">
                <div v-if="detailLoading" class="empty-state">
                  <Loader2 class="h-4 w-4 animate-spin" />
                  Loading messages
                </div>

                <article
                  v-for="message in sortedMessages"
                  :key="message.id"
                  class="chat-row"
                  :class="{ 'chat-row-outbound': isOutboundMessage(message) }"
                >
                  <div class="chat-avatar">{{ message.from.slice(0, 1).toUpperCase() || '?' }}</div>
                  <div class="chat-stack">
                    <div class="chat-meta">
                      <span>{{ message.from }}</span>
                      <span>{{ formatMessageTime(message) }}</span>
                      <span>{{ message.msgtype }}</span>
                    </div>
                    <div class="chat-bubble">
                      <p>{{ textFromBody(message.body) }}</p>
                    </div>
                    <p class="chat-id">{{ message.msgid }}</p>

                    <div
                      v-for="delivery in messageDeliveries.get(message.id)"
                      :key="delivery.id"
                      class="chat-delivery"
                    >
                      <div class="chat-bubble chat-bubble-agent">
                        <p>{{ textFromDelivery(delivery) }}</p>
                      </div>
                      <div class="chat-delivery-actions">
                        <span>Delivery #{{ delivery.id }} · {{ deliveryStatusLabel(delivery.status) }}</span>
                        <button
                          v-if="delivery.status === 0"
                          class="small-button"
                          :disabled="ackingDeliveryId === delivery.id"
                          title="Ack delivery"
                          @click="ackDelivery(delivery.id)"
                        >
                          <Loader2 v-if="ackingDeliveryId === delivery.id" class="h-3.5 w-3.5 animate-spin" />
                          <Check v-else class="h-3.5 w-3.5" />
                          Ack
                        </button>
                      </div>
                    </div>
                  </div>
                </article>

                <div v-if="!detailLoading && !sortedMessages.length" class="empty-state">No messages in this channel.</div>
              </div>

              <form class="chat-composer" @submit.prevent="injectMessage">
                <label class="chat-identity">
                  <span>as</span>
                  <input v-model="chatUserId" placeholder="guest" />
                </label>
                <textarea
                  v-model="injectText"
                  class="chat-composer-input"
                  :placeholder="selectedRoom ? `Message #${selectedRoom.room.channel_room_id}` : 'Choose a channel first'"
                  rows="1"
                />
                <button class="chat-send-button" :disabled="injecting || !injectText.trim()" title="Send message">
                    <Loader2 v-if="injecting" class="h-4 w-4 animate-spin" />
                    <MessageSquarePlus v-else class="h-4 w-4" />
                    Send
                  </button>
              </form>
            </section>

            <section v-else-if="activeTab === 'memory'" class="grid gap-4">
              <div class="flex items-center justify-between gap-3">
                <div class="flex items-center gap-2">
                  <Database class="h-4 w-4 text-accent" />
                  <h3 class="text-sm font-700">Channel Memory</h3>
                </div>
                <select v-model="memoryStatus" class="field text-sm">
                  <option value="active">Active</option>
                  <option value="inactive">Inactive</option>
                  <option value="all">All</option>
                </select>
              </div>

              <div v-if="memoryLoading" class="empty-state">
                <Loader2 class="h-4 w-4 animate-spin" />
                Loading memory
              </div>
              <div v-else class="memory-grid">
                <article v-for="item in memoryItems" :key="item.id" class="memory-row">
                  <div class="flex items-center justify-between gap-3">
                    <span class="badge-muted">{{ item.type }}</span>
                    <span class="text-xs text-muted">{{ formatDate(item.updated_at) }}</span>
                  </div>
                  <h4 class="mt-3 break-words text-sm font-700">{{ item.key }}</h4>
                  <p class="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-muted">{{ item.content }}</p>
                </article>
              </div>
              <div v-if="!memoryLoading && !memoryItems.length" class="empty-state">No memory items.</div>
            </section>

            <section v-else-if="activeTab === 'agents'" class="grid gap-4">
              <form class="side-panel agent-detail grid gap-3" @submit.prevent="saveAgent">
                <h3 class="text-sm font-700">{{ selectedAgentId ? 'Edit Agent' : 'New Agent' }}</h3>
                <div class="grid grid-cols-2 gap-2">
                  <label class="grid gap-1 text-xs text-muted">
                    Key
                    <input v-model="agentKey" class="field" placeholder="product" />
                  </label>
                  <label class="grid gap-1 text-xs text-muted">
                    Display Name
                    <input v-model="agentDisplayName" class="field" placeholder="Product" />
                  </label>
                </div>
                <div class="grid grid-cols-2 gap-2">
                  <label class="grid gap-1 text-xs text-muted">
                    Creator
                    <input v-model="agentOwnerId" class="field" placeholder="guest" />
                  </label>
                  <label class="flex items-end gap-2 pb-2 text-sm text-muted">
                    <input v-model="agentShared" type="checkbox" />
                    Share with channels
                  </label>
                </div>
                <label class="grid gap-1 text-xs text-muted">
                  Description
                  <input v-model="agentDescription" class="field" />
                </label>
                <label class="grid gap-1 text-xs text-muted">
                  Prompt
                  <textarea v-model="agentPrompt" class="field min-h-48 resize-y" />
                </label>
                <label class="grid gap-1 text-xs text-muted">
                  Allowed Tools JSON
                  <textarea v-model="agentAllowedTools" class="field min-h-24 resize-y font-mono text-xs" />
                </label>
                <label class="flex items-center gap-2 text-sm text-muted">
                  <input v-model="agentEnabled" type="checkbox" />
                  Enabled
                </label>
                <button class="primary-button justify-center" :disabled="savingAgent || !agentKey.trim() || !agentDisplayName.trim() || !agentPrompt.trim()">
                  <Loader2 v-if="savingAgent" class="h-4 w-4 animate-spin" />
                  <Check v-else class="h-4 w-4" />
                  {{ agentShared ? 'Save Shared Agent' : 'Save Private Agent' }}
                </button>
              </form>
            </section>

            <section v-else-if="activeTab === 'settings'" class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
              <form class="side-panel grid gap-3" @submit.prevent="saveRoomSettings">
                <h3 class="text-sm font-700">Channel Settings</h3>
                <div class="grid grid-cols-2 gap-2">
                  <label class="grid gap-1 text-xs text-muted">
                    Channel
                    <input v-model="settingsChannel" class="field" />
                  </label>
                  <label class="grid gap-1 text-xs text-muted">
                    Type
                    <select v-model="settingsChannelRoomType" class="field">
                      <option value="group">Group</option>
                      <option value="direct">Direct</option>
                    </select>
                  </label>
                </div>
                <label class="grid gap-1 text-xs text-muted">
                    Channel ID
                  <input v-model="settingsChannelRoomId" class="field" />
                </label>
                <label class="grid gap-1 text-xs text-muted">
                  Display Name
                  <input v-model="settingsDisplayName" class="field" />
                </label>
                <label class="grid gap-1 text-xs text-muted">
                  Display Alias
                  <input v-model="settingsOutboundAlias" class="field" />
                </label>
                <label class="flex items-center gap-2 text-sm text-muted">
                  <input v-model="settingsAgentEnabled" type="checkbox" />
                  Agent enabled
                </label>
                <label class="grid gap-1 text-xs text-muted">
                  Trigger Policy JSON
                  <textarea v-model="settingsTriggerPolicy" class="field min-h-32 resize-y font-mono text-xs" />
                </label>
                <button
                  class="primary-button justify-center"
                  :disabled="savingRoom || !settingsChannel.trim() || !settingsChannelRoomId.trim() || !settingsChannelRoomType.trim()"
                >
                  <Loader2 v-if="savingRoom" class="h-4 w-4 animate-spin" />
                  <Check v-else class="h-4 w-4" />
                  {{ selectedRoom ? 'Save Channel' : 'Join Channel' }}
                </button>
              </form>

              <div v-if="selectedRoom" class="settings-grid">
                <div class="settings-row">
                  <span>Internal ID</span>
                  <code>{{ selectedRoom.room.id }}</code>
                </div>
                <div class="settings-row">
                  <span>Tenant</span>
                  <code>{{ selectedRoom.room.tenant_id }}</code>
                </div>
                <div class="settings-row">
                  <span>Agent session</span>
                  <code>{{ selectedRoom.agent_session?.id || 'None' }}</code>
                </div>
                <div class="settings-row">
                  <span>Trigger boundary</span>
                  <code>{{ selectedRoom.agent_session?.pending_trigger_message_id || 0 }}</code>
                </div>
                <div class="settings-row">
                  <span>Caught-up boundary</span>
                  <code>{{ selectedRoom.agent_session?.caught_up_message_id || 0 }}</code>
                </div>
              </div>
              <div v-else class="empty-state">
                <DoorOpen class="h-4 w-4" />
                Fill the form to join a channel.
              </div>
            </section>
          </div>
        </div>

        <div v-else class="grid h-full place-items-center p-6">
          <div class="grid justify-items-center gap-3">
            <div class="empty-state px-8">Connect, then join a channel to start chatting.</div>
            <div class="flex gap-2">
              <button class="primary-button" type="button" :disabled="!canUseAPI" @click="newRoom">
                <Plus class="h-4 w-4" />
                Join Channel
              </button>
              <button class="small-button" type="button" :disabled="!canUseAPI" @click="openAgents">
                <Bot class="h-3.5 w-3.5" />
                Manage Agents
              </button>
            </div>
          </div>
        </div>
      </section>
    </section>
  </main>
</template>
