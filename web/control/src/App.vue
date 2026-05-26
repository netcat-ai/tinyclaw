<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useStorage } from '@vueuse/core'
import {
  Check,
  Database,
  KeyRound,
  Loader2,
  MessageSquarePlus,
  RefreshCw,
  Search,
  ShieldCheck,
} from 'lucide-vue-next'
import {
  api,
  type Credentials,
  type Delivery,
  type InjectMessageInput,
  type MemoryItem,
  type Message,
  type RoomSummary,
  type Timeline,
} from './api'

type Tab = 'timeline' | 'memory' | 'settings'

const savedCredentials = useStorage<Credentials>('tinyclaw.control.credentials', {
  clientId: 'admin',
  clientSecret: '',
})

const clientId = ref(savedCredentials.value.clientId)
const clientSecret = ref(savedCredentials.value.clientSecret)
const rememberCredentials = useStorage('tinyclaw.control.remember_credentials', true)
const rooms = ref<RoomSummary[]>([])
const selectedRoomId = useStorage<number | null>('tinyclaw.control.selected_room_id', null)
const timeline = ref<Timeline | null>(null)
const memoryItems = ref<MemoryItem[]>([])
const activeTab = ref<Tab>('timeline')
const query = ref('')
const roomLoading = ref(false)
const detailLoading = ref(false)
const memoryLoading = ref(false)
const authError = ref('')
const actionError = ref('')
const senderId = ref('admin')
const senderName = ref('Admin')
const injectText = ref('')
const suppressAgentTrigger = ref(false)
const injecting = ref(false)
const ackingDeliveryId = ref<number | null>(null)
const memoryStatus = ref('active')
const memoryTypes = ref<string[]>([])

const credentials = computed<Credentials>(() => ({
  clientId: clientId.value.trim(),
  clientSecret: clientSecret.value,
}))

const selectedRoom = computed(() =>
  rooms.value.find((summary) => summary.room.id === selectedRoomId.value) ?? null,
)

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

const formatDate = (value?: string) => {
  if (!value || value.startsWith('0001-')) return 'Never'
  return new Intl.DateTimeFormat(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

const textFromPayload = (payload: Record<string, unknown>) => {
  const text = payload.text
  if (typeof text === 'string' && text.trim()) return text
  return JSON.stringify(payload)
}

const deliveryStatusLabel = (status: number) => {
  if (status === 0) return 'Pending'
  if (status === 1) return 'Acked'
  if (status === 2) return 'Failed'
  return `Status ${status}`
}

const loadRooms = async () => {
  if (!canUseAPI.value) return
  roomLoading.value = true
  authError.value = ''
  try {
    const result = await api.listRooms(credentials.value)
    rooms.value = result.rooms
    if (!selectedRoomId.value && result.rooms.length) {
      selectedRoomId.value = result.rooms[0].room.id
    }
  } catch (error) {
    authError.value = error instanceof Error ? error.message : 'Failed to load rooms'
  } finally {
    roomLoading.value = false
  }
}

const loadTimeline = async () => {
  if (!canUseAPI.value || !selectedRoomId.value) return
  detailLoading.value = true
  actionError.value = ''
  try {
    timeline.value = await api.getTimeline(credentials.value, selectedRoomId.value)
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
    memoryItems.value = result.items
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

const refreshCurrent = async () => {
  await loadRooms()
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
}

const injectMessage = async () => {
  if (!selectedRoomId.value || !injectText.value.trim()) return
  injecting.value = true
  actionError.value = ''
  try {
    const input: InjectMessageInput = {
      sender_id: senderId.value.trim() || 'admin',
      sender_name: senderName.value.trim() || 'Admin',
      text: injectText.value.trim(),
      suppress_agent_trigger: suppressAgentTrigger.value,
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

watch(activeTab, (tab) => {
  if (tab === 'memory') void loadMemory()
})

watch(memoryStatus, () => {
  if (activeTab.value === 'memory') void loadMemory()
})

onMounted(async () => {
  if (canUseAPI.value) {
    await loadRooms()
    await loadTimeline()
  }
})
</script>

<template>
  <main class="min-h-screen flex flex-col bg-panel text-ink">
    <header class="h-14 flex items-center justify-between border-b border-line bg-surface px-5">
      <div class="flex items-center gap-3">
        <ShieldCheck class="h-5 w-5 text-accent" />
        <div>
          <h1 class="text-base font-700">TinyClaw Control</h1>
          <p class="text-xs text-muted">Clawman admin read models and operator actions</p>
        </div>
      </div>
      <button class="icon-button" title="Refresh" :disabled="roomLoading || detailLoading" @click="refreshCurrent">
        <Loader2 v-if="roomLoading || detailLoading" class="h-4 w-4 animate-spin" />
        <RefreshCw v-else class="h-4 w-4" />
      </button>
    </header>

    <section v-if="!canUseAPI || authError" class="border-b border-line bg-surface px-5 py-3">
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

    <section class="app-shell grid min-h-0 flex-1 grid-cols-[340px_minmax(0,1fr)]">
      <aside class="min-h-0 border-r border-line bg-surface">
        <div class="border-b border-line p-4">
          <div class="mb-3 flex items-center justify-between">
            <h2 class="text-sm font-700">Rooms</h2>
            <span class="text-xs text-muted">{{ filteredRooms.length }} / {{ rooms.length }}</span>
          </div>
          <label class="flex items-center gap-2 rounded-6px border border-line bg-white px-3 py-2">
            <Search class="h-4 w-4 text-muted" />
            <input v-model="query" class="min-w-0 flex-1 bg-transparent text-sm outline-none" placeholder="Search rooms" />
          </label>
        </div>

        <div class="room-list h-[calc(100vh-150px)] overflow-y-auto">
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
                {{ summary.room.channel }} / {{ summary.room.channel_room_type }} / {{ summary.room.outbound_alias }}
              </span>
            </span>
            <span class="grid justify-items-end gap-1">
              <span v-if="summary.pending_delivery_count" class="badge-warn">{{ summary.pending_delivery_count }}</span>
              <span class="text-xs text-muted">{{ formatDate(summary.last_message_time) }}</span>
            </span>
          </button>

          <div v-if="!filteredRooms.length" class="p-4 text-sm text-muted">
            No rooms loaded.
          </div>
        </div>
      </aside>

      <section class="min-w-0 min-h-0">
        <div v-if="selectedRoom" class="grid h-full grid-rows-[auto_auto_minmax(0,1fr)]">
          <div class="border-b border-line bg-surface px-5 py-4">
            <div class="flex items-start justify-between gap-4">
              <div class="min-w-0">
                <h2 class="truncate text-xl font-750">
                  {{ selectedRoom.room.display_name || selectedRoom.room.channel_room_id }}
                </h2>
                <p class="mt-1 text-sm text-muted">
                  #{{ selectedRoom.room.id }} · {{ selectedRoom.room.channel }} · {{ selectedRoom.room.channel_room_id }}
                </p>
              </div>
              <div class="flex gap-2 text-xs">
                <span class="status-pill">{{ selectedRoom.agent_session?.enabled ? 'Agent enabled' : 'Agent disabled' }}</span>
                <span class="status-pill">{{ pendingDeliveries.length }} pending</span>
              </div>
            </div>
          </div>

          <div class="border-b border-line bg-surface px-5">
            <nav class="flex gap-1">
              <button class="tab-button" :class="{ 'tab-button-active': activeTab === 'timeline' }" @click="activeTab = 'timeline'">
                Timeline
              </button>
              <button class="tab-button" :class="{ 'tab-button-active': activeTab === 'memory' }" @click="activeTab = 'memory'">
                Memory
              </button>
              <button class="tab-button" :class="{ 'tab-button-active': activeTab === 'settings' }" @click="activeTab = 'settings'">
                Settings
              </button>
            </nav>
          </div>

          <div class="min-h-0 overflow-y-auto p-5">
            <p v-if="actionError" class="mb-4 rounded-6px border border-danger/25 bg-white px-3 py-2 text-sm text-danger">
              {{ actionError }}
            </p>

            <section v-if="activeTab === 'timeline'" class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
              <div class="grid gap-3">
                <div v-if="detailLoading" class="empty-state">
                  <Loader2 class="h-4 w-4 animate-spin" />
                  Loading timeline
                </div>

                <article v-for="message in sortedMessages" :key="message.id" class="message-row">
                  <div class="flex items-start justify-between gap-3">
                    <div class="min-w-0">
                      <div class="flex flex-wrap items-center gap-2">
                        <span class="text-sm font-700">{{ message.sender_name || message.sender_id }}</span>
                        <span class="text-xs text-muted">#{{ message.id }}</span>
                        <span v-if="message.skipped" class="badge-muted">Skipped</span>
                      </div>
                      <p class="mt-2 whitespace-pre-wrap break-words text-sm leading-6">{{ textFromPayload(message.payload) }}</p>
                      <p class="mt-2 text-xs text-muted">{{ formatDate(message.message_time) }} · {{ message.source_message_id }}</p>
                    </div>
                  </div>

                  <div v-if="messageDeliveries.get(message.id)?.length" class="mt-3 grid gap-2">
                    <div v-for="delivery in messageDeliveries.get(message.id)" :key="delivery.id" class="delivery-row">
                      <div class="min-w-0">
                        <span class="text-xs font-700">Delivery #{{ delivery.id }}</span>
                        <span class="ml-2 text-xs text-muted">{{ deliveryStatusLabel(delivery.status) }}</span>
                      </div>
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
                </article>

                <div v-if="!detailLoading && !sortedMessages.length" class="empty-state">No messages in this room.</div>
              </div>

              <aside class="side-panel">
                <div class="mb-3 flex items-center gap-2">
                  <MessageSquarePlus class="h-4 w-4 text-accent" />
                  <h3 class="text-sm font-700">Inject Message</h3>
                </div>
                <form class="grid gap-3" @submit.prevent="injectMessage">
                  <div class="grid grid-cols-2 gap-2">
                    <label class="grid gap-1 text-xs text-muted">
                      Sender ID
                      <input v-model="senderId" class="field" />
                    </label>
                    <label class="grid gap-1 text-xs text-muted">
                      Sender Name
                      <input v-model="senderName" class="field" />
                    </label>
                  </div>
                  <label class="grid gap-1 text-xs text-muted">
                    Text
                    <textarea v-model="injectText" class="field min-h-28 resize-y" />
                  </label>
                  <label class="flex items-center gap-2 text-sm text-muted">
                    <input v-model="suppressAgentTrigger" type="checkbox" />
                    Suppress agent trigger
                  </label>
                  <button class="primary-button justify-center" :disabled="injecting || !injectText.trim()">
                    <Loader2 v-if="injecting" class="h-4 w-4 animate-spin" />
                    <MessageSquarePlus v-else class="h-4 w-4" />
                    Inject
                  </button>
                </form>
              </aside>
            </section>

            <section v-else-if="activeTab === 'memory'" class="grid gap-4">
              <div class="flex items-center justify-between gap-3">
                <div class="flex items-center gap-2">
                  <Database class="h-4 w-4 text-accent" />
                  <h3 class="text-sm font-700">Room Memory</h3>
                </div>
                <select v-model="memoryStatus" class="field text-sm">
                  <option value="active">Active</option>
                  <option value="stale">Stale</option>
                  <option value="closed">Closed</option>
                  <option value="">All</option>
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

            <section v-else class="settings-grid">
              <div class="settings-row">
                <span>Room ID</span>
                <code>{{ selectedRoom.room.id }}</code>
              </div>
              <div class="settings-row">
                <span>Tenant</span>
                <code>{{ selectedRoom.room.tenant_id }}</code>
              </div>
              <div class="settings-row">
                <span>Outbound alias</span>
                <code>{{ selectedRoom.room.outbound_alias }}</code>
              </div>
              <div class="settings-row">
                <span>Agent session</span>
                <code>{{ selectedRoom.agent_session?.id || 'None' }}</code>
              </div>
            </section>
          </div>
        </div>

        <div v-else class="grid h-full place-items-center p-6">
          <div class="empty-state">Connect with admin credentials to load rooms.</div>
        </div>
      </section>
    </section>
  </main>
</template>
