export type Credentials = {
  clientId: string
  clientSecret: string
}

export type Room = {
  id: number
  tenant_id: string
  channel: string
  channel_room_id: string
  channel_room_type: string
  display_name?: string
  outbound_alias: string
}

export type AgentSession = {
  id: number
  room_id: number
  enabled: boolean
  trigger_policy?: unknown
  pending_trigger_message_id?: number
  caught_up_message_id: number
  codex_session_id?: string
}

export type Message = {
  id: number
  room_id: number
  source_message_id: string
  source: string
  sender_id: string
  sender_name?: string
  payload: Record<string, unknown>
  message_time: string
  created_at: string
}

export type Delivery = {
  id: number
  room_id: number
  agent_session_id: number
  source_message_from_id: number
  source_message_to_id: number
  payload: Record<string, unknown>
  status: number
  created_at?: string
  acked_at?: string
}

export type MemoryItem = {
  id: number
  room_id: number
  type: string
  key: string
  content: string
  status: string
  source_message_from_id: number
  source_message_to_id: number
  updated_at: string
}

export type RoomSummary = {
  room: Room
  agent_session?: AgentSession
  pending_delivery_count: number
  last_message_time?: string
  updated_at: string
}

export type Timeline = {
  room: Room
  agent_sessions: AgentSession[]
  messages: Message[]
  deliveries: Delivery[]
  has_more: boolean
}

export type RegisterRoomInput = {
  channel: string
  channel_room_id: string
  channel_room_type: string
  display_name: string
  outbound_alias: string
  agent_enabled: boolean
  trigger_policy?: unknown
}

export type InjectMessageInput = {
  sender_id: string
  sender_name: string
  text: string
  suppress_agent_trigger: boolean
}

export type Agent = {
  id: number
  key: string
  display_name: string
  description?: string
  prompt: string
  allowed_tools: unknown
  enabled: boolean
  created_at: string
  updated_at: string
}

export type UpsertAgentInput = {
  key: string
  display_name: string
  description: string
  prompt: string
  allowed_tools: unknown
  enabled: boolean
}

export type RegisterRoomResult = {
  room: Room
  agent_session: AgentSession
}

const authHeader = (credentials: Credentials) => {
  const raw = `${credentials.clientId}:${credentials.clientSecret}`
  return `Basic ${btoa(unescape(encodeURIComponent(raw)))}`
}

export const requestJSON = async <T>(
  path: string,
  credentials: Credentials,
  init: RequestInit = {},
): Promise<T> => {
  const response = await fetch(path, {
    ...init,
    headers: {
      Authorization: authHeader(credentials),
      'Content-Type': 'application/json',
      ...(init.headers ?? {}),
    },
  })
  const text = await response.text()
  const payload = text ? JSON.parse(text) : null
  if (!response.ok) {
    const detail = payload?.error?.detail ?? response.statusText
    throw new Error(detail)
  }
  return payload as T
}

export const api = {
  listRooms: (credentials: Credentials) =>
    requestJSON<{ rooms: RoomSummary[] }>('/admin/api/rooms?limit=200', credentials),

  listAgents: (credentials: Credentials) =>
    requestJSON<{ agents: Agent[] }>('/admin/api/agents', credentials),

  createAgent: (credentials: Credentials, input: UpsertAgentInput) =>
    requestJSON<{ agent: Agent }>('/admin/api/agents', credentials, {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  updateAgent: (credentials: Credentials, agentId: number, input: UpsertAgentInput) =>
    requestJSON<{ agent: Agent }>(`/admin/api/agents/${agentId}`, credentials, {
      method: 'PUT',
      body: JSON.stringify(input),
    }),

  getTimeline: (credentials: Credentials, roomId: number, beforeMessageId?: number) => {
    const params = new URLSearchParams({ limit: '100' })
    if (beforeMessageId) params.set('before_message_id', String(beforeMessageId))
    return requestJSON<Timeline>(`/admin/api/rooms/${roomId}/timeline?${params}`, credentials)
  },

  listMemory: (credentials: Credentials, roomId: number, status: string, types: string[]) => {
    const params = new URLSearchParams({ status, limit: '200' })
    if (types.length) params.set('types', types.join(','))
    return requestJSON<{ items: MemoryItem[] }>(`/admin/api/rooms/${roomId}/memory?${params}`, credentials)
  },

  registerRoom: (credentials: Credentials, input: RegisterRoomInput) =>
    requestJSON<RegisterRoomResult>('/admin/api/rooms', credentials, {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  injectMessage: (credentials: Credentials, roomId: number, input: InjectMessageInput) =>
    requestJSON(`/admin/api/rooms/${roomId}/messages:inject`, credentials, {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  ackDelivery: (credentials: Credentials, deliveryId: number) =>
    requestJSON(`/admin/api/deliveries/${deliveryId}/ack`, credentials, {
      method: 'POST',
      body: '{}',
    }),
}
