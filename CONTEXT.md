# TinyClaw

TinyClaw is a room-scoped agent runtime that lets enterprise users interact with isolated agents from external communication channels.

## Language

**Room**:
A stable TinyClaw-owned conversation container mapped one-to-one from a **Channel Room** in the first room model.
_Avoid_: Agent context, sandbox identity

**Channel Room**:
A conversation identity as named by an external channel.
_Avoid_: Room, internal room

**Agent Session**:
A **Room**'s default long-running agent context in the first execution model.
_Avoid_: Table requirement, channel room

**Sandbox**:
A replaceable execution instance assigned to exactly one **Agent Session** during its lifecycle.
_Avoid_: Worker, consumer, bot instance

**Tool Runtime Backend**:
A provider adapter that executes sandboxed tool calls for an **Agent Session**.
_Avoid_: Invocation executor, core dispatch logic, channel adapter

**Runtime Session**:
A backend-specific execution runtime reused by one **Room**'s implicit **Agent Session**.
_Avoid_: Core table, Invocation, Channel Room

**Clawman**:
The control plane and message gateway that owns external channels, persistence, scheduling, governance, and delivery.
_Avoid_: WeCom bot, agent process

**Agent**:
The configured reasoning persona and capability set used by an **Agent Session**.
_Avoid_: Channel adapter, message consumer

**Channel Adapter**:
An external service that translates a third-party platform into TinyClaw's inbound and outbound API.
_Avoid_: Core protocol, runtime logic

**Message**:
A TinyClaw-owned inbound fact associated with exactly one **Room**.
_Avoid_: Channel cursor, WeCom seq

**Source Message ID**:
The idempotency key assigned by a **Channel Adapter** to one external inbound message.
_Avoid_: Message id, cursor

**Invocation**:
One real agent execution attempt inside an **Agent Session**.
_Avoid_: Message, task, scheduler job

**Delivery**:
A channel-bound outbound item produced from an **Invocation**.
_Avoid_: Job, reply row

**Trigger Policy**:
A room-level rule set that decides whether an inbound **Message** should start or join an **Invocation**.
_Avoid_: Channel adapter rule, global bot setting

**Channel Cursor**:
An external-channel checkpoint used only by a **Channel Adapter** to resume ingestion.
_Avoid_: Clawman state, Message id, processing seq

## Relationships

- In the first execution model, a **Room** has exactly one implicit default **Agent Session**.
- An **Agent Session** belongs to exactly one **Room**.
- An **Agent Session** may be served by many **Sandboxes** over time.
- A **Sandbox** serves exactly one **Agent Session** during its lifecycle.
- A **Tool Runtime Backend** owns backend-specific **Runtime Sessions**.
- In the first execution model, a **Runtime Session** is scoped to one **Room** and reused across tool calls, but its identity is private to the **Tool Runtime Backend**.
- **Clawman** assigns **Sandboxes** to **Agent Sessions** and routes standardized messages between external **Channel Adapters** and **Agents**.
- An **Agent** runs inside a **Sandbox** and acts through one **Agent Session**.
- A **Channel Adapter** belongs outside `clawman` and owns third-party platform protocol state.
- In the first version, each **Channel Room** maps to exactly one **Room**, and each **Room** belongs to exactly one **Channel Room**.
- A **Message** belongs to exactly one **Room**.
- A **Source Message ID** is unique within one tenant, channel, and **Channel Room**.
- An **Invocation** belongs to exactly one **Agent Session**.
- An **Invocation** may produce zero or more **Deliveries**.
- A **Delivery** targets exactly one external channel destination.
- A **Trigger Policy** belongs to a **Room**; when absent, `clawman` uses the channel default.
- A **Channel Cursor** belongs to a **Channel Adapter**, not to `clawman` or a **Message**.

## Example dialogue

> **Dev:** "Do we need an `agent_sessions` table in the first implementation?"
> **Domain expert:** "No. The first implementation uses the **Room** as the storage key for the implicit default **Agent Session**; multiple agent mentions are treated as input for that session, not as separate scheduler lanes."

## Flagged ambiguities

- "group" was used to mean both external channel group chat and internal runtime boundary; resolved: use **Room** for the internal boundary.
- "bot" was used to mean both **Clawman** and **Agent**; resolved: **Clawman** owns platform governance, while **Agent** owns room behavior.
- "room_id" was used to mean both an external channel conversation id and a TinyClaw-owned identity; resolved: **Room** is internal, while **Channel Room** is external.
- Multiple **Channel Rooms** must not be merged into one **Room** in the first version; resolved: shared context is out of scope for the first room model.
- Enterprise WeCom `seq` was used as both **Message** identity and **Channel Cursor**; resolved: **Message** identity is TinyClaw-owned, while channel checkpoints stay adapter-local.
- WeCom-specific ingestion was considered part of `clawman`; resolved: third-party platform ingestion belongs to external **Channel Adapters**, and `clawman` receives standardized inbound messages.
- Duplicate inbound delivery from **Channel Adapters** is expected; resolved: every inbound **Message** must carry a **Source Message ID** for idempotent insertion.
- **Room** was used as both external conversation container and agent execution boundary; resolved: **Room** is the conversation container, while **Agent Session** is the execution and long-running context boundary.
- Multiple external agent mentions were considered as separate **Agent Sessions**; resolved: first version treats them as prompt content inside the default **Agent Session**.
- An explicit `agent_sessions` table was considered for the first implementation; resolved: first version keeps the default **Agent Session** implicit and stores execution state by **Room**.
- `jobs` was used for outbound replies; resolved: use **Delivery** as the domain term, with `jobs` treated as a legacy implementation detail.
- Trigger logic was considered adapter-owned; resolved: **Channel Adapters** standardize messages, while `clawman` applies each **Room**'s **Trigger Policy** or channel default.
- The sandbox module was considered as clawman core logic; resolved: use **Tool Runtime Backend** as the abstraction, with provider-specific **Runtime Sessions** such as E2B or agent-sandbox.
- A shared `runtime_sessions` core table was considered; resolved: first version keeps **Runtime Session** persistence, caching, and external ids inside each **Tool Runtime Backend**.
- Sandbox was considered as an **Invocation** executor; resolved: the agent reasoning loop stays in `clawman`, and sandbox backends only execute tool calls.
