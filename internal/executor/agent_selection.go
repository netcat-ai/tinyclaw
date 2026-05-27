package executor

import "tinyclaw/internal/core"

func messageTexts(messages []core.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := extractMessageText(message.Payload)
		if text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}
