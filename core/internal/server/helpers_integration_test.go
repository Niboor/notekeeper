//go:build integration

package server_test

import (
	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/getkin/kin-openapi/openapi3"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

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
