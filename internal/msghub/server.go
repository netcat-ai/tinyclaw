package msghub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Store interface {
	Authenticate(ctx context.Context, token string) (Actor, bool, error)
	AppendTerminalMessage(ctx context.Context, actor Actor, input SendMessageRequest) (Message, bool, error)
	CreateReplyTask(ctx context.Context, actor Actor, input SendMessageRequest) (ActorTask, error)
	ListUpdates(ctx context.Context, actor Actor, afterID int64, limit int) ([]UpdateView, error)
	Ack(ctx context.Context, actor Actor, input AckRequest) (AckResponse, error)
}

type Server struct {
	store Store
	mux   *http.ServeMux
}

func NewServer(store Store) *Server {
	server := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	server.registerRoutes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/ilink/sendmessage", s.handleSendMessage)
	s.mux.HandleFunc("/ilink/getupdates", s.handleGetUpdates)
	s.mux.HandleFunc("/ilink/ack", s.handleAck)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	actor, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input SendMessageRequest
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch actor.Kind {
	case ActorKindTerminal:
		message, duplicate, err := s.store.AppendTerminalMessage(r.Context(), actor, input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "append_message_failed", err.Error())
			return
		}
		view := messageToView(message)
		writeJSON(w, http.StatusOK, SendMessageResponse{
			Accepted:  true,
			Duplicate: duplicate,
			Message:   &view,
		})
	case ActorKindAIAgent:
		task, err := s.store.CreateReplyTask(r.Context(), actor, input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "create_task_failed", err.Error())
			return
		}
		view := taskToView(task)
		writeJSON(w, http.StatusOK, SendMessageResponse{
			Accepted: true,
			Task:     &view,
		})
	default:
		writeError(w, http.StatusForbidden, "unsupported_actor", fmt.Sprintf("sendmessage is not supported for %s actor", actor.Kind))
	}
}

func (s *Server) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	actor, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input GetUpdatesRequest
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updates, err := s.store.ListUpdates(r.Context(), actor, input.AfterID, input.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "getupdates_failed", err.Error())
		return
	}
	if updates == nil {
		updates = []UpdateView{}
	}
	writeJSON(w, http.StatusOK, GetUpdatesResponse{Updates: updates})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	actor, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input AckRequest
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	response, err := s.store.Ack(r.Context(), actor, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ack_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return Actor{}, false
	}
	actor, ok, err := s.store.Authenticate(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return Actor{}, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
		return Actor{}, false
	}
	return actor, true
}

func bearerToken(value string) string {
	value = strings.TrimSpace(value)
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

func readJSON(r *http.Request, target any) error {
	defer func() { _ = r.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		data = []byte(`{}`)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]string{
		"error":  code,
		"detail": detail,
	})
}
