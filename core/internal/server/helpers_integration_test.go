//go:build integration

package server_test

import (
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/store"
)

func botsInput(name, domain string) bots.CreateInstanceInput {
	return bots.CreateInstanceInput{Type: "matrix", Name: name, IdentityDomain: domain}
}

func storeActor() store.Actor { return store.Actor{Kind: "system"} }
