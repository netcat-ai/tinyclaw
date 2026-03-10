package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"
	"tinyclaw/worktool"
)

const targetPrefix = "wecom:target:"

type egressRequest struct {
	RoomID   string `json:"room_id"`
	TenantID string `json:"tenant_id"`
	ChatType string `json:"chat_type"`
	Reply    struct {
		Text string `json:"text"`
	} `json:"reply"`
}

type EgressServer struct {
	token    string
	redis    *redis.Client
	worktool *worktool.Client
	mux      *http.ServeMux
}

func NewEgressServer(token string, rdb *redis.Client, wt *worktool.Client) *EgressServer {
	s := &EgressServer{
		token:    token,
		redis:    rdb,
		worktool: wt,
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /egress", s.handleEgress)
	return s
}

func (s *EgressServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *EgressServer) handleEgress(w http.ResponseWriter, r *http.Request) {
	// Bearer token auth
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req egressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.RoomID == "" || req.Reply.Text == "" {
		http.Error(w, "room_id and reply.text are required", http.StatusBadRequest)
		return
	}

	// Look up target name from Redis
	target, err := s.redis.Get(r.Context(), targetPrefix+req.RoomID).Result()
	if err != nil {
		slog.Error("egress target lookup failed", "room_id", req.RoomID, "err", err)
		http.Error(w, "target not found for room_id", http.StatusNotFound)
		return
	}

	if err := s.worktool.SendTextMessage(target, req.Reply.Text, nil); err != nil {
		slog.Error("egress send failed", "room_id", req.RoomID, "target", target, "err", err)
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}

	slog.Info("egress sent", "room_id", req.RoomID, "target", target)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
