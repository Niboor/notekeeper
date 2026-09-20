//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/jobs"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/store"
)

func botsInput(name, domain string) bots.CreateInstanceInput {
	return bots.CreateInstanceInput{Type: "matrix", Name: name, IdentityDomain: domain}
}

func storeActor() store.Actor { return store.Actor{Kind: "system"} }

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func httptestServer(h http.Handler) *httptest.Server { return httptest.NewServer(h) }

func userapiSpec() (*openapi3.T, error) { return userapi.GetSpec() }

func accountsCreate(name string) accounts.CreateUserInput {
	return accounts.CreateUserInput{Username: name}
}

func (s *stack) lookupUser(name string) uuid.UUID {
	s.t.Helper()
	var id uuid.UUID
	if err := s.db.Admin.QueryRow(context.Background(), `select id from users where username = $1`, name).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

type httpRequest = http.Request

// botDoWith is botDo with extra request headers.
func (s *stack) botDoWith(bearer, method, path string, body any, headers map[string]string) response {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.bot.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return response{Status: res.StatusCode, Header: res.Header, Body: b}
}

// runHousekeeping runs the daily cleanup job as the scheduler would.
func (s *stack) runHousekeeping() error {
	return jobs.Purge(context.Background(), jobs.Deps{Store: s.st, Now: time.Now})
}
