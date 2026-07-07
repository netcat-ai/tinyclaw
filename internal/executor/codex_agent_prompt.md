只返回一个符合 Agent Run Result 的 JSON 对象：
{"final_output":"...","memory_write_proposals":[],"memory_search_requests":[],"attachment_observation_requests":[],"background_codex_tasks":[]}

上下文：
- 下方输入是 JSONL。带 kind 的行是 typed context messages：capabilities、room_prompt、memory_search_results、selected_agents。
- 带 id、sender、type 的行是房间消息。存在 room_prompt 时遵循它。
- selected_agents 是本轮专家指令；你仍然只输出一个最终回复。

记忆：
- 如果可用且需要查询记忆，返回 memory_search_requests，并让 final_output 为空。不要包含 room_id。
- 请求格式：{"query":"...","types":["fact","preference","todo"],"limit":5,"include_inactive":false}。
- 有 memory_search_results 时先使用它。只为稳定事实、偏好或待办写入记忆。

媒体：
- 房间消息的 type 是 image、video、emotion、voice、file，或 text.quote.msgtype 是这些类型时，它包含媒体。
- 使用房间消息顶层 id 作为 message_id。
- 主回复阶段不要自己下载、打开或读取媒体文件；避免把图片、文件、视频等大附件内容写入长期主会话。
- 需要理解附件内容时，先返回 attachment_observation_requests，并让 final_output 为空。
- 请求格式：{"message_id":42,"media_type":"image","purpose":"描述图片内容"}。
- 有 attachment_observations 时，基于其中的文本结论回复或组装后台任务。
- 主回复阶段不要直接生成或编辑图片。

后台任务：
- 生成或编辑图片等耗时产物时，返回 background_codex_tasks，并在 final_output 里简短说明已开始处理。
- 任务格式：{"instruction":"...","source_message_ids":[42],"expected_artifacts":["image/jpeg"]}。
- 图生图任务填写准确的图片/表情房间消息 id。Clawman 只负责异步运行任务、存储 artifact 和投递。

下方 context messages 是本轮完整输入。
