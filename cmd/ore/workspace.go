// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/orevcs/ore/internal/workspace"
)

// The workstation commands (ADR 0016 §2): a developer works on a repository
// with no git on the machine. `ore open` materializes the active area,
// `ore save` records edits as a patch set of a change, `ore sync` refreshes
// the tree, `ore land` asks for landing under the repository's autonomy
// level, `ore status` shows the ledger's view and what is pending locally;
// `ore login` stores the token for a server.

func runWorkspace(cmd string, args []string, cwd string, stdin io.Reader, out io.Writer) error {
	fl := parseFlags(args)
	ctx := context.Background()
	switch cmd {
	case "login":
		return runLogin(ctx, fl, stdin, out)
	case "open":
		return runOpen(ctx, fl, cwd, out)
	}
	ws, err := workspace.Find(cwd)
	if err != nil {
		return err
	}
	token, _ := workspace.TokenFor(ws.Server)
	if t := fl.str("token", ""); t != "" {
		token = t
	}
	if token == "" {
		return fmt.Errorf("no credentials for %s: set ORE_TOKEN or run `ore login %s`", ws.Server, ws.Server)
	}
	c := workspace.NewClient(ws.Server, token)
	switch cmd {
	case "save":
		msg := fl.str("m", fl.str("message", ""))
		if msg == "" && len(fl.pos) > 0 {
			msg = strings.Join(fl.pos, " ")
		}
		_, err := ws.Save(ctx, c, msg, out)
		return err
	case "sync":
		_, err := ws.Sync(ctx, c, fl.bool("inline"), out)
		return err
	case "land":
		res, err := ws.Land(ctx, c, out)
		if err != nil {
			return err
		}
		if res.Outcome != "landed" && res.Outcome != "queued" {
			return errNotLanded
		}
		return nil
	case "status":
		return ws.Status(ctx, c, out)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// errNotLanded makes `ore land` exit 1 when the change did not land, without
// a second message: the outcome line already said why.
var errNotLanded = errors.New("")

func runOpen(ctx context.Context, fl *flags, cwd string, out io.Writer) error {
	if len(fl.pos) < 1 {
		return errors.New("usage: ore open ORG/REPO [DIR] [--all] [--url URL]")
	}
	org, name, err := splitRepo(fl.pos[0])
	if err != nil {
		return err
	}
	dir := name
	if len(fl.pos) > 1 {
		dir = fl.pos[1]
	}
	if !strings.HasPrefix(dir, "/") {
		dir = cwd + "/" + dir
	}
	server := workspace.NormalizeServer(fl.str("url", envOr("ORE_URL", "http://localhost:7777")))
	token, user := workspace.TokenFor(server)
	if t := fl.str("token", ""); t != "" {
		token = t
	}
	if token == "" {
		return fmt.Errorf("no credentials for %s: set ORE_TOKEN or run `ore login %s`", server, server)
	}
	c := workspace.NewClient(server, token)
	if user == "" {
		if u, err := c.Whoami(ctx); err == nil && u != "" {
			user = u
		} else if err != nil && workspace.Status(err) != 404 {
			return err
		}
	}
	if user == "" {
		user = workspace.LocalUser()
	}
	_, err = workspace.Open(ctx, c, org, name, dir, fl.bool("all"), user, out)
	return err
}

func runLogin(ctx context.Context, fl *flags, stdin io.Reader, out io.Writer) error {
	if len(fl.pos) < 1 {
		return errors.New("usage: ore login URL [--token T]   (or pipe the token on stdin)")
	}
	server := workspace.NormalizeServer(fl.pos[0])
	token := fl.str("token", os.Getenv("ORE_TOKEN"))
	if token == "" {
		if f, ok := stdin.(*os.File); ok {
			if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
				fmt.Fprint(os.Stderr, "token (shown as you type; or pass --token, or pipe it): ")
			}
		}
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no token given")
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		return errors.New("no token given")
	}
	c := workspace.NewClient(server, token)
	user, err := c.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("login %s: %w", server, err)
	}
	creds, err := workspace.LoadCredentials()
	if err != nil {
		return err
	}
	creds.Set(server, token, user)
	if err := creds.Save(); err != nil {
		return err
	}
	if user == "" {
		user = workspace.LocalUser()
	}
	fmt.Fprintf(out, "logged in to %s as %s (%s)\n", server, user, workspace.CredentialsPath())
	return nil
}
