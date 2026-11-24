package identity

import (
	"context"
	"errors"
	"log/slog"
)

// A passkey is made at /setup with an approval code (ADR 0007): the page starts an approval of purpose passkey, the owner
// approves it with `kedalio-core approve` (never from a client), the poll then returns the creation options, and the
// registration is accepted here. It is optional, and there may be more than one.

// errNoPasskeyApproval is every way the approval for a passkey can be of no use.
var errNoPasskeyApproval = errors.New("the approval is not valid: get a new code on the setup page and approve it on the server")

// spendApproval makes an approved approval of the purpose done, once: false when it is not approved, is of another
// purpose, expired, or already spent.
func (a *Auth) spendApproval(ctx context.Context, waiter, purpose string) (bool, error) {
	res, err := a.db.ExecContext(ctx,
		"update approvals set state = 'done' where waiter = ? and purpose = ? and state = 'approved' and expires > ?",
		hash(waiter), purpose, a.now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// passkeyAccept keeps a passkey the owner approved. The registration is checked before the approval is spent, so a bad
// one does not cost the owner the approval, and the approval is spent in one conditional update, so two registrations
// cannot both win.
func (i *Identity) passkeyAccept(ctx context.Context, waiter string, reg Registration, label string) error {
	cred, err := i.Passkeys.Check(ctx, reg)
	if err != nil {
		return err
	}
	ok, err := i.Auth.spendApproval(ctx, waiter, PurposePasskey)
	if err != nil {
		return err
	}
	if !ok {
		return errNoPasskeyApproval
	}
	if err := i.Passkeys.Add(ctx, cred, label); err != nil {
		return err
	}
	// the owner's record that one was made, since a relayed code could have made one they did not mean to (no secret in it)
	slog.Info("identity: passkey added", "id", truncate(cred.ID, 8), "name", cleanLine(label, 60))
	return nil
}
