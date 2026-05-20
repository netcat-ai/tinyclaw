# TinyClaw

TinyClaw is a room-scoped agent runtime that lets enterprise users interact with isolated agents from external communication channels.

## Language

**Room**:
A stable TinyClaw-owned conversation container mapped one-to-one from a **Channel Room** in the first room model.
_Avoid_: Agent context, sandbox identity

**Channel Room**:
A conversation identity as named by an external channel.
_Avoid_: Room, internal room

**Registered Room**:
A **Room** explicitly admitted into TinyClaw before inbound messages may be processed for agent execution.
_Avoid_: Auto-created room, discovered room

**Channel Room Display Name**:
The human-facing name for a **Channel Room** as understood by TinyClaw.
_Avoid_: Sender name, outbound alias

**Outbound Target Alias**:
The channel-adapter-facing label used to locate a **Channel Room** for sending.
_Avoid_: Room name, sender name

**Send-Ready Room**:
A **Room** whose **Channel Room** can be reliably targeted for outbound delivery.
_Avoid_: Ingested room, known room

**Agent-Enabled Session**:
An **Agent Session** whose inbound messages may trigger agent execution.
_Avoid_: Known room, send-ready room

**Agent Session**:
A long-running agent context inside a **Room** with its own processing progress.
_Avoid_: Room, Agent Run

**Sandbox**:
A future replaceable tool-execution instance. It is not the current agent executor.
_Avoid_: Worker, consumer, bot instance

**Tool Runtime Backend**:
A provider adapter that executes sandboxed tool calls for an **Agent Session**.
_Avoid_: Agent runner, core dispatch logic, channel adapter

**Runtime Session**:
A backend-specific execution runtime reused by one **Room**'s implicit **Agent Session**.
_Avoid_: Core table, Agent Run, Channel Room

**Clawman**:
The control plane that owns Core Model persistence, trigger decisions, agent execution, and delivery outbox APIs.
_Avoid_: WeCom bot, agent process

**Agent**:
The configured reasoning behavior used by an **Agent Runner**.
_Avoid_: Channel adapter, message consumer

**Agent Runner**:
A local executor adapter that runs one **Agent Run** and returns final output. The current concrete runner is `CodexRunner`.
_Avoid_: Channel adapter, sandbox backend

**Channel Adapter**:
An external service that translates a third-party platform into TinyClaw's inbound and outbound API.
_Avoid_: Core protocol, runtime logic

**Message**:
A TinyClaw-owned inbound fact associated with exactly one **Room**.
_Avoid_: Channel cursor, WeCom seq

**Source Message ID**:
The idempotency key assigned by a **Channel Adapter** to one external inbound message.
_Avoid_: Message id, cursor

**Agent Run**:
One execution attempt for an **Agent Session** over a bounded **Message** window.
_Avoid_: Message, task, scheduler job

**Delivery**:
A channel-bound outbound item produced from an **Agent Run**.
_Avoid_: Job, reply row

**Trigger Policy**:
A session-level rule set that decides whether an inbound **Message** should advance an **Agent Session** trigger boundary.
_Avoid_: Channel adapter rule, global bot setting

**Scheduled Message**:
A planned agent input that belongs to one **Agent Session** and becomes a **Message** when due.
_Avoid_: Agent run task, scheduler job, reminder row

**Schedule**:
A durable plan owned by one **Agent Session** for producing one or more **Scheduled Messages** over time.
_Avoid_: Cron job, task row, automation

**Channel Cursor**:
An external-channel checkpoint used only by a **Channel Adapter** to resume ingestion.
_Avoid_: Clawman state, Message id, processing seq

## Relationships

- A **Room** may have one or more **Agent Sessions**.
- An **Agent Session** belongs to exactly one **Room**.
- A future **Sandbox** may serve one **Agent Session** during its lifecycle, but current agent execution does not require a sandbox.
- A **Tool Runtime Backend** owns backend-specific **Runtime Sessions**.
- In the first execution model, a **Runtime Session** is scoped to one **Room** and reused across tool calls, but its identity is private to the **Tool Runtime Backend**.
- **Clawman** accepts standardized messages from external **Channel Adapters**, advances **Agent Session** trigger boundaries, runs the configured **Agent Runner**, and exposes **Deliveries** for outbound adapters.
- The current **Agent Runner** runs in-process by invoking local Codex CLI; future sandbox backends should be limited to tool execution unless a new design changes that boundary.
- A **Channel Adapter** belongs outside `clawman` and owns third-party platform protocol state.
- In the first version, each **Channel Room** maps to exactly one **Room**, and each **Room** belongs to exactly one **Channel Room**.
- A **Registered Room** is required before a **Channel Adapter** may process inbound messages for agent execution.
- A **Channel Adapter** may register a **Room** when it can provide the required **Channel Room** identity and outbound targeting information.
- After registration, inbound **Messages** are addressed to the **Room** rather than creating the **Room** as an inbound side effect.
- A **Channel Room Display Name** belongs to one **Channel Room** and may change over time.
- An **Outbound Target Alias** belongs to one **Channel Room** and may differ from the **Channel Room Display Name**.
- A **Send-Ready Room** has enough outbound targeting information for a **Channel Adapter** to send a **Delivery**.
- An **Agent-Enabled Session** may be auto-registered by a **Channel Adapter** when the adapter can provide a **Channel Room Display Name** and **Outbound Target Alias**.
- A **Message** belongs to exactly one **Room**.
- A **Source Message ID** is unique within one tenant, channel, and **Channel Room**.
- An **Agent Run** belongs to exactly one **Agent Session**.
- An **Agent Run** may produce zero or more **Deliveries**.
- A **Delivery** targets exactly one external channel destination.
- A **Trigger Policy** belongs to an **Agent Session**; when absent, `clawman` uses the channel default.
- A **Schedule** belongs to exactly one **Agent Session**.
- A **Schedule** may produce zero or more **Scheduled Messages**.
- A **Scheduled Message** belongs to exactly one **Agent Session**.
- A **Schedule** created from an agent interaction belongs to the **Agent Session** handling that interaction.
- A **Channel Cursor** belongs to a **Channel Adapter**, not to `clawman` or a **Message**.

## Example dialogue

> **Dev:** "When a group message says '虾虾', do we create a new execution row?"
> **Domain expert:** "No — we update the matching **Agent Session** trigger boundary; the execution loop runs an **Agent Run** over the message window."

## Flagged ambiguities

- "group" was used to mean both external channel group chat and internal runtime boundary; resolved: use **Room** for the internal boundary.
- "bot" was used to mean both **Clawman** and **Agent**; resolved: **Clawman** owns platform governance, while **Agent** owns room behavior.
- "room_id" was used to mean both an external channel conversation id and a TinyClaw-owned identity; resolved: **Room** is internal, while **Channel Room** is external.
- Multiple **Channel Rooms** must not be merged into one **Room** in the first version; resolved: shared context is out of scope for the first room model.
- Enterprise WeCom `seq` was used as both **Message** identity and **Channel Cursor**; resolved: **Message** identity is TinyClaw-owned, while channel checkpoints stay adapter-local.
- WeCom-specific ingestion was considered part of `clawman`; resolved: third-party platform ingestion belongs to external **Channel Adapters**, and `clawman` receives standardized inbound messages.
- Duplicate inbound delivery from **Channel Adapters** is expected; resolved: every inbound **Message** must carry a **Source Message ID** for idempotent insertion.
- **Room** was used as both external conversation container and agent execution boundary; resolved: **Room** is the conversation container, while **Agent Session** is the execution and long-running context boundary.
- Multiple agents in one **Room** require distinct **Agent Sessions**; resolved: each **Agent Session** tracks its own processing progress.
- `jobs` was used for outbound replies; resolved: use **Delivery** as the domain term, with `jobs` treated as a legacy implementation detail.
- Trigger logic was considered adapter-owned; resolved: **Channel Adapters** standardize messages, while `clawman` applies each **Room**'s **Trigger Policy** or channel default.
- The sandbox module was considered as clawman core logic; resolved: use **Tool Runtime Backend** as the abstraction, with provider-specific **Runtime Sessions** such as E2B or agent-sandbox.
- A shared `runtime_sessions` core table was considered; resolved: first version keeps **Runtime Session** persistence, caching, and external ids inside each **Tool Runtime Backend**.
- Sandbox was considered as an **Agent Runner**; resolved: the agent reasoning loop stays in `clawman`, and sandbox backends only execute tool calls.
- "room name" was used to mean both display context and outbound UI targeting; resolved: use **Channel Room Display Name** for human-facing context and **Outbound Target Alias** for send targeting.
- A room was considered usable after inbound identity was known; resolved: a **Room** must be **Send-Ready** before agent execution can produce meaningful outbound results.
- **Room** creation was considered an inbound message side effect; resolved: a **Room** should be a **Registered Room** before TinyClaw processes messages for agent execution.
- External-contact rooms were considered manual-confirmation-only; resolved: a **Channel Adapter** may auto-register and enable them when it can provide a name suitable for outbound targeting.
- "scheduled task" was used to mean both a durable schedule definition and the agent input produced at run time; resolved: use **Scheduled Message** for the agent-facing input, owned by an **Agent Session**.
- "scheduled task" was also used to mean the durable recurring plan; resolved: use **Schedule** for the managed plan that produces **Scheduled Messages**.
- A **Schedule** was considered as a direct wake signal for an **Agent Session**; resolved: a due **Schedule** first produces a **Scheduled Message**, and that message participates in normal trigger processing.
- Cross-room schedule creation was considered; resolved: a **Schedule** created during an agent interaction belongs to the current **Agent Session** only.
