package ownerclient

import (
	"context"
	"encoding/json"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type memoryStore struct {
	session Session
	saves   int
}

func (s *memoryStore) Load() (Session, error)   { return s.session, nil }
func (s *memoryStore) Save(value Session) error { s.session = value; s.saves++; return nil }
func TestRefreshPersistsBeforeOwnerRequestAndNeverRetriesMutation(t *testing.T) {
	store := &memoryStore{session: Session{RefreshHandle: "synthetic-refresh"}}
	submits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session/refresh" {
			json.NewEncoder(w).Encode(Session{AccessHandle: "synthetic-access", RefreshHandle: "synthetic-rotated", AccessExpiresAt: time.Now().Add(time.Hour)})
			return
		}
		if store.saves != 1 || r.Header.Get("Authorization") != "Bearer "+store.session.AccessHandle {
			t.Error("refresh not durably saved before owner call")
		}
		submits++
		w.WriteHeader(503)
		w.Write([]byte(`{"state":"unconfirmed"}`))
	}))
	defer server.Close()
	client := New(store)
	client.BaseURL = server.URL
	out, err := client.Submit(context.Background(), "lock", "stable-synthetic-id", contract.CommandOptions{})
	if err == nil || out.State != "unconfirmed" || submits != 1 || store.saves != 1 {
		t.Fatal("uncertain mutation retried or lost state")
	}
}
