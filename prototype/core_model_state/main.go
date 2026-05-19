package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"tinyclaw/prototype/core_model_state/model"
)

const (
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
	reset = "\x1b[0m"
)

func main() {
	state := model.NewState()
	reader := bufio.NewReader(os.Stdin)

	for {
		render(state)
		input, _ := reader.ReadString('\n')
		switch strings.TrimSpace(input) {
		case "c":
			state = model.InboundContext(state)
		case "t":
			state = model.InboundTrigger(state)
		case "s":
			state = model.InboundSkipped(state)
		case "d":
			state = model.InboundDuplicate(state)
		case "r":
			state = model.MarkRunning(state)
		case "o":
			state = model.CompleteWithOutput(state)
		case "e":
			state = model.CompleteEmpty(state)
		case "f":
			state = model.FailActive(state)
		case "a":
			state = model.AckDelivery(state)
		case "k":
			state = model.Tick(state)
		case "q":
			fmt.Println("bye")
			return
		default:
			state.LastEvent = "unknown command"
		}
	}
}

func render(state model.State) {
	fmt.Print("\033[2J\033[H")
	fmt.Printf("%sTinyClaw core model prototype%s\n", bold, reset)
	fmt.Printf("%sQuestion: does dispatch_state + invocation + delivery feel right?%s\n\n", dim, reset)
	fmt.Printf("%sLast event:%s %s\n", bold, reset, state.LastEvent)
	fmt.Printf("%sClock:%s %s\n\n", bold, reset, state.Now.Format("15:04:05"))

	fmt.Printf("%sRooms%s\n", bold, reset)
	if len(state.Rooms) == 0 {
		fmt.Printf("  %snone%s\n", dim, reset)
	}
	for _, room := range state.Rooms {
		fmt.Printf("  #%d tenant=%s channel=%s channel_room=%s type=%s\n",
			room.ID, room.TenantID, room.Channel, room.ChannelRoomID, room.ChannelRoomType)
	}

	fmt.Printf("\n%sMessages%s\n", bold, reset)
	if len(state.Messages) == 0 {
		fmt.Printf("  %snone%s\n", dim, reset)
	}
	for _, message := range state.Messages {
		fmt.Printf("  #%d room=%d state=%s source=%s sender=%s text=%q\n",
			message.ID, message.RoomID, dispatchLabel(message.DispatchState), message.SourceMessageID, message.SenderID, message.Text)
	}

	fmt.Printf("\n%sInvocations%s\n", bold, reset)
	if len(state.Invocations) == 0 {
		fmt.Printf("  %snone%s\n", dim, reset)
	}
	for _, invocation := range state.Invocations {
		fmt.Printf("  #%d room=%d status=%s trigger_message=%d\n",
			invocation.ID, invocation.RoomID, invocationStatusLabel(invocation.Status), invocation.TriggerMessageID)
		fmt.Printf("     input=%s\n", invocation.InputSnapshot)
		if invocation.OutputSnapshot != "" {
			fmt.Printf("     output=%s\n", invocation.OutputSnapshot)
		}
	}

	fmt.Printf("\n%sDeliveries%s\n", bold, reset)
	if len(state.Deliveries) == 0 {
		fmt.Printf("  %snone%s\n", dim, reset)
	}
	for _, delivery := range state.Deliveries {
		fmt.Printf("  #%d seq=%d room=%d invocation=%d status=%s payload=%q\n",
			delivery.ID, delivery.Seq, delivery.RoomID, delivery.InvocationID, deliveryStatusLabel(delivery.Status), delivery.Payload)
	}

	fmt.Printf("\n%sKeys%s\n", bold, reset)
	fmt.Printf("  %sc%s context msg   %st%s trigger msg   %ss%s skipped msg   %sd%s duplicate\n", bold, reset, bold, reset, bold, reset, bold, reset)
	fmt.Printf("  %sr%s running       %so%s complete+out  %se%s complete empty %sf%s fail\n", bold, reset, bold, reset, bold, reset, bold, reset)
	fmt.Printf("  %sa%s ack delivery  %sk%s tick           %sq%s quit\n", bold, reset, bold, reset, bold, reset)
	fmt.Print("\n> ")
}

func invocationStatusLabel(value int16) string {
	switch value {
	case model.InvocationStatusQueued:
		return "0(queued)"
	case model.InvocationStatusRunning:
		return "1(running)"
	case model.InvocationStatusCompleted:
		return "2(completed)"
	case model.InvocationStatusFailed:
		return "3(failed)"
	case model.InvocationStatusCancelled:
		return "4(cancelled)"
	default:
		return fmt.Sprintf("%d(unknown)", value)
	}
}

func deliveryStatusLabel(value int16) string {
	switch value {
	case model.DeliveryStatusPending:
		return "0(pending)"
	case model.DeliveryStatusAcked:
		return "1(acked)"
	case model.DeliveryStatusFailed:
		return "2(failed)"
	default:
		return fmt.Sprintf("%d(unknown)", value)
	}
}

func dispatchLabel(value int64) string {
	switch value {
	case model.DispatchWaiting:
		return "0(waiting)"
	case model.DispatchSkipped:
		return "1(skipped)"
	default:
		return fmt.Sprintf("%d(invocation)", value)
	}
}
