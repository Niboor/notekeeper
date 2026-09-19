package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/app"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/db"
	"github.com/Niboor/notekeeper/core/internal/store"
)

const adminUsage = `usage: core admin <command>

  bootstrap <username>                      create the admin account and print its activation link
  link <username>                           print a new activation link (lost password, recovery)
  bot-create <type> <name> [domain]         register a bot instance (domain: e.g. the Matrix server name)
  bot-credential <instance name>            create a credential for a bot instance; the secret is shown once
`

// admin runs the operator commands. They talk to the database directly (through kubectl exec or
// docker compose run), so there is no bootstrap secret to configure or leak (design decision D4).
func admin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		fmt.Fprint(os.Stderr, adminUsage)
		return errors.New("missing command")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.RequireDatabase(); err != nil {
		return err
	}
	if err := cfg.RequireAuth(); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)
	sv, err := app.NewServices(cfg, st, newLogger(cfg.LogLevel))
	if err != nil {
		return err
	}
	svc, botSvc := sv.Accounts, sv.Bots
	cmd, rest := fs.Arg(0), fs.Args()[1:]

	printLink := func(l accounts.ActivationLink) {
		fmt.Printf("Activation link (valid until %s, single use):\n  %s/activate#%s\n", l.Expires.Format("2006-01-02 15:04 MST"), cfg.AppURL, l.Token)
		if cfg.AppURL == "" {
			fmt.Println("  (set NK_APP_URL to print the full address; append the path to your app's address)")
		}
	}
	switch cmd {
	case "bootstrap":
		if len(rest) != 1 {
			return errors.New("usage: core admin bootstrap <username>")
		}
		link, err := svc.Bootstrap(ctx, rest[0])
		if err != nil {
			return err
		}
		printLink(link)
	case "link":
		if len(rest) != 1 {
			return errors.New("usage: core admin link <username>")
		}
		link, err := svc.ActivationLinkFor(ctx, rest[0])
		if err != nil {
			return err
		}
		printLink(link)
	case "bot-create":
		if len(rest) < 2 || len(rest) > 3 {
			return errors.New("usage: core admin bot-create <type> <name> [identity domain]")
		}
		domain := ""
		if len(rest) == 3 {
			domain = rest[2]
		}
		inst, err := botSvc.CreateInstance(ctx, store.Actor{Kind: "system"}, bots.CreateInstanceInput{Type: rest[0], Name: rest[1], IdentityDomain: domain})
		if err != nil {
			return err
		}
		fmt.Printf("Created bot instance %q (%s). Create a credential with: core admin bot-credential %s\n", inst.Name, inst.ID, inst.Name)
	case "bot-credential":
		if len(rest) != 1 {
			return errors.New("usage: core admin bot-credential <instance name>")
		}
		insts, err := botSvc.ListInstances(ctx)
		if err != nil {
			return err
		}
		for _, i := range insts {
			if i.Name == rest[0] {
				c, err := botSvc.CreateCredential(ctx, store.Actor{Kind: "system"}, i.ID, nil)
				if err != nil {
					return err
				}
				fmt.Printf("Bot key for %q (shown once, store it as a secret):\n  %s\n", i.Name, c.Bearer)
				return nil
			}
		}
		return fmt.Errorf("no bot instance named %q", rest[0])
	default:
		fmt.Fprint(os.Stderr, adminUsage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}
