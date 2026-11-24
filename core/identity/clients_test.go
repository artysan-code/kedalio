package identity

import (
	"net/url"
	"testing"
)

// Revoking a client ends all its access at once: its access token is refused on the next MCP call, its refresh token
// buys nothing, and the client is gone, so it has to register and get the owner's passkey again. Another client is
// left alone.
func TestRevokingAClientRefusesItsTokensAndForgetsIt(t *testing.T) {
	r := newRig(t)
	r.setupPasskey(t)
	cloud := r.connectAs(t, "Claude", "https://claude.ai/api/mcp/auth_callback")
	local := r.connectAs(t, "Claude Code", "http://127.0.0.1:43210/callback")
	a := r.idn.Auth

	list, err := a.Clients(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("clients: %v %v", list, err)
	}
	var c ClientInfo
	for _, x := range list {
		if x.Name == "Claude" {
			c = x
		}
	}
	if !c.Remote || c.Host != "claude.ai" || c.Active != 2 || c.Created == "" {
		t.Fatalf("the cloud client is listed as %+v", c)
	}

	// a refresh token as claude.ai holds it
	tx, _ := a.db.BeginTx(ctx, nil)
	pair, err := a.pair(ctx, tx, c.ID, "fam")
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	refresh, _ := pair["refresh_token"].(string)

	ok, err := a.RevokeClient(ctx, c.ID)
	if err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if cs, err := r.mcpClient(t, cloud); err == nil {
		cs.Close()
		t.Error("the revoked client's access token still opens an MCP session")
	}
	if rep, _ := a.Token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); rep.Status != 400 {
		t.Errorf("a refresh after the revocation: %d %v", rep.Status, rep.JSON)
	}
	if cl, _ := a.client(ctx, c.ID); cl != nil {
		t.Error("the client is still registered")
	}
	if cs, err := r.mcpClient(t, local); err != nil {
		t.Errorf("the other client lost access: %v", err)
	} else {
		cs.Close()
	}
	if ok, _ := a.RevokeClient(ctx, c.ID); ok {
		t.Error("revoking an unknown client says it did something")
	}
}

// A client names itself at the anonymous registration, so the name is data from outside: Clients drops control
// characters, or a name could rewrite the owner's terminal or forge rows in `clients list`.
func TestClientsDropsControlCharactersFromANameOnTheOwnersTerminal(t *testing.T) {
	r := newRig(t)
	a := r.idn.Auth
	if _, err := a.db.ExecContext(ctx, `insert into oauth_clients values ('evil', ?, '[]', '2026-11-01T10:00:00Z')`,
		"x\x1b[2Jy\nforged\rrow\t\u0085"); err != nil {
		t.Fatal(err)
	}
	list, err := a.Clients(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("clients: %v %v", list, err)
	}
	if list[0].Name != "x[2Jyforgedrow" {
		t.Errorf("the name is %q", list[0].Name)
	}
}
