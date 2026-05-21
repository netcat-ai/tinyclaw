# Use Room-Owned Memory

TinyClaw stores durable memory as **Room Memory** owned by a **Room**, not by an **Agent Session**, runner session, or sandbox. **Agent Sessions** may search **Room Memory** during **Agent Runs** and may produce **Memory Write Proposals** after a run, but Clawman governs scope, audit, and persistence. This keeps long-term knowledge aligned with the conversation container while allowing runners such as Codex or Claude to remain replaceable.
