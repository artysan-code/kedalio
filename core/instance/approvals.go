package instance

import (
	"context"
	"errors"

	"kedalio/core/identity"
	"kedalio/core/mcp"
)

// approvals gives the MCP tool clients_approve the identity's approval codes. The packages do not import each other, so
// this is where they meet.
type approvals struct{ auth *identity.Auth }

func (a approvals) Show(ctx context.Context, code string) (mcp.ApprovalView, error) {
	v, err := a.auth.ShowApproval(ctx, code)
	if err != nil {
		return mcp.ApprovalView{}, gone(err)
	}
	return mcp.ApprovalView{Purpose: v.Purpose, ClientName: v.ClientName, Kind: v.Kind, RedirectHost: v.RedirectHost,
		IP: v.IP, Local: v.Local, Age: v.Age, State: v.State}, nil
}

func (a approvals) Approve(ctx context.Context, code, by string) error {
	_, err := a.auth.Decide(ctx, code, true, by)
	return gone(err)
}

// gone maps a code that is of no use, in whichever way, to the one error the tool answers for.
func gone(err error) error {
	if errors.Is(err, identity.ErrNoApproval) || errors.Is(err, identity.ErrNotACode) {
		return mcp.ErrNoApproval
	}
	return err
}
