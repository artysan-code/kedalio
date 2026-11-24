package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// Approval codes (ADR 0007): a new client is authorized when the owner approves the short code its sign-in page shows,
// with `kedalio-core approve` on the server or, for a local client, with the clients_approve tool. A code is a
// single-use handle that lives minutes, so it is stored hashed like every other secret here, and it is never logged
// or returned by a tool.

const (
	// ApprovalTTL is how long a code lives: the owner has to find a terminal or a connected client.
	ApprovalTTL = 10 * time.Minute
	// MaxPendingApprovals caps the live undecided requests. Nothing is evicted: a flood must not push out the owner's own.
	// MaxPendingPerIP keeps one address from filling that cap alone; MaxPendingPasskeys is lower for passkeys, which are
	// durable and made rarely.
	MaxPendingApprovals = 20
	MaxPendingPerIP     = 3
	MaxPendingPasskeys  = 2

	approvalAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ" // no 0, O, 1, I, L: a typo must not land on another code
	approvalLen      = 8
	maxParamLen      = 2048
	maxAgentLen      = 200
)

// Purposes of an approval.
const (
	PurposeOAuth   = "oauth"
	PurposePasskey = "passkey"
)

var (
	// ErrNoApproval is every way a code can be of no use (unknown, expired, already decided or spent): which one is
	// not told.
	ErrNoApproval = errors.New("no such approval: it is unknown, expired or already used")
	// ErrNotACode is input that cannot be a code.
	ErrNotACode = errors.New("that is not an approval code")
	// ErrTooManyApprovals is the cap of MaxPendingApprovals.
	ErrTooManyApprovals = errors.New("too many waiting approvals")
	// ErrTooManyFromAddress is the cap of MaxPendingPerIP.
	ErrTooManyFromAddress = errors.New("too many waiting approvals from your address")
	// ErrTooManyPasskeyApprovals is the cap of MaxPendingPasskeys.
	ErrTooManyPasskeyApprovals = errors.New("too many waiting passkey approvals")
)

// InvalidRequestError is an authorization request that cannot be approved; Message is what the page shows.
type InvalidRequestError struct{ Message string }

func (e *InvalidRequestError) Error() string { return e.Message }

// newApprovalCode is eight symbols of the alphabet, shown XXXX-XXXX.
func newApprovalCode() string {
	// 31 does not divide 256: bytes from 248 up are dropped so that every symbol is as likely as any other
	const limit = 256 - 256%len(approvalAlphabet)
	var sb strings.Builder
	buf := make([]byte, 16)
	for sb.Len() < approvalLen+1 {
		if _, err := rand.Read(buf); err != nil {
			panic("identity: no randomness: " + err.Error())
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			if sb.Len() == 4 {
				sb.WriteByte('-')
			}
			sb.WriteByte(approvalAlphabet[int(b)%len(approvalAlphabet)])
			if sb.Len() == approvalLen+1 {
				break
			}
		}
	}
	return sb.String()
}

// normalizeCode is the code as kept (upper case, no dash or spaces); ok is false for what cannot be a code.
func normalizeCode(s string) (string, bool) {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != approvalLen || strings.Trim(s, approvalAlphabet) != "" {
		return "", false
	}
	return s, true
}

// ApprovalView is what the approver sees before deciding. The client's name is its own word, never a fact.
type ApprovalView struct {
	Purpose string
	// ClientName is cleaned of control characters (it may reach a terminal); empty for a passkey.
	ClientName string
	// Kind is "cloud: claude.ai", "local program" or, for a passkey, "passkey".
	Kind string
	// Local is a client on this machine (loopback redirect): the only kind the tool may approve.
	Local        bool
	RedirectHost string
	// IP and Agent are the requesting browser's.
	IP, Agent string
	Age       time.Duration
	// State is pending, approved or denied.
	State string
}

// cleanLine is a caller's text made safe for a terminal and a log: no control characters, cut short.
func cleanLine(s string, n int) string {
	return truncate(strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return ' '
		}
		return r
	}, s), n)
}

// StartApproval begins an approval and returns the code to show, the secret that only the requesting browser polls
// with, and when it expires. For purpose oauth the request is checked as at consent, so what is approved is valid.
func (a *Auth) StartApproval(ctx context.Context, purpose string, params url.Values, ip, agent string) (code, waiter string, expires time.Time, err error) {
	stored := map[string]string{}
	client := ""
	switch purpose {
	case PurposeOAuth:
		for _, v := range params {
			if len(v) > 0 && len(v[0]) > maxParamLen {
				return "", "", time.Time{}, &InvalidRequestError{"the request is too long"}
			}
		}
		c, message, err := a.CheckAuthorize(ctx, params)
		if err != nil {
			return "", "", time.Time{}, err
		}
		if c == nil {
			return "", "", time.Time{}, &InvalidRequestError{message}
		}
		client = c.ID
		for _, k := range []string{"client_id", "redirect_uri", "code_challenge", "state", "resource"} {
			if v := params.Get(k); v != "" {
				stored[k] = v
			}
		}
	case PurposePasskey:
	default:
		return "", "", time.Time{}, errors.New("unknown approval purpose")
	}
	now := a.now()
	ip = cleanLine(ip, 64)
	expires = now.Add(ApprovalTTL)
	waiter = randomToken(32)
	// counted and inserted in one transaction (immediate: it holds the write lock), so neither two requests at once nor
	// the CLI's process can take the same last slot
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer tx.Rollback()
	var live, fromIP, passkeys int
	if err := tx.QueryRowContext(ctx,
		`select count(*), coalesce(sum(ip = ?), 0), coalesce(sum(purpose = 'passkey'), 0)
		   from approvals where state = 'pending' and expires > ?`, ip, now.UnixMilli()).Scan(&live, &fromIP, &passkeys); err != nil {
		return "", "", time.Time{}, err
	}
	switch {
	case purpose == PurposePasskey && passkeys >= MaxPendingPasskeys:
		return "", "", time.Time{}, ErrTooManyPasskeyApprovals
	case fromIP >= MaxPendingPerIP:
		return "", "", time.Time{}, ErrTooManyFromAddress
	case live >= MaxPendingApprovals:
		return "", "", time.Time{}, ErrTooManyApprovals
	}
	for try := 0; try < 5; try++ {
		code = newApprovalCode()
		norm, _ := normalizeCode(code)
		_, err = tx.ExecContext(ctx,
			`insert into approvals (hash, waiter, purpose, params, client, ip, agent, created, expires)
			 values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hash(norm), hash(waiter), purpose, mustJSON(stored), client, ip, cleanLine(agent, maxAgentLen),
			now.UnixMilli(), expires.UnixMilli())
		// a clash with a live code is a 1 in 10^12 chance; a new one is drawn
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			break
		}
	}
	if err != nil {
		return "", "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", "", time.Time{}, err
	}
	return code, waiter, expires, nil
}

// ShowApproval is what a code asks for. ErrNoApproval when it is unknown, expired or spent.
func (a *Auth) ShowApproval(ctx context.Context, code string) (ApprovalView, error) {
	norm, ok := normalizeCode(code)
	if !ok {
		return ApprovalView{}, ErrNotACode
	}
	now := a.now()
	var v ApprovalView
	var name, redirects string
	var created int64
	err := a.db.QueryRowContext(ctx,
		`select a.purpose, coalesce(c.name, ''), coalesce(c.redirects, ''), a.ip, a.agent, a.created, a.state
		   from approvals a left join oauth_clients c on c.id = a.client
		  where a.hash = ? and a.expires > ? and a.state in ('pending', 'approved', 'denied')`,
		hash(norm), now.UnixMilli()).Scan(&v.Purpose, &name, &redirects, &v.IP, &v.Agent, &created, &v.State)
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovalView{}, ErrNoApproval
	}
	if err != nil {
		return ApprovalView{}, err
	}
	v.Age = max(0, now.Sub(time.UnixMilli(created)))
	if v.Purpose == PurposePasskey {
		v.Kind = "passkey"
		return v, nil
	}
	var list []string
	if redirects == "" || fromJSON(redirects, &list) != nil {
		// the client went (revoked): nothing is left to approve
		return ApprovalView{}, ErrNoApproval
	}
	v.ClientName = CleanName(name)
	v.RedirectHost, v.Kind, v.Local = describeClient(list)
	return v, nil
}

// describeClient is where a client's redirects point and what kind of client that makes it.
func describeClient(redirects []string) (host, kind string, local bool) {
	host = redirectHost(redirects)
	if local = !isRemote(redirects); local {
		return host, "local program", true
	}
	return host, "cloud: " + host, false
}

// PendingApproval is a request still waiting, as `kedalio-core approve -list` shows it: never its code.
type PendingApproval struct {
	// ID is a short handle for the line, from the waiter's hash: it is not the code and opens nothing.
	ID, Purpose, Kind, ClientName, Host, IP string
	Age                                     time.Duration
}

// PendingApprovals lists the live requests no one has decided, oldest first.
func (a *Auth) PendingApprovals(ctx context.Context) ([]PendingApproval, error) {
	now := a.now()
	rows, err := a.db.QueryContext(ctx,
		`select substr(a.waiter, 1, 8), a.purpose, coalesce(c.name, ''), coalesce(c.redirects, ''), a.ip, a.created
		   from approvals a left join oauth_clients c on c.id = a.client
		  where a.state = 'pending' and a.expires > ? order by a.created, a.waiter`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingApproval
	for rows.Next() {
		var p PendingApproval
		var name, redirects string
		var created int64
		if err := rows.Scan(&p.ID, &p.Purpose, &name, &redirects, &p.IP, &created); err != nil {
			return nil, err
		}
		p.Age = max(0, now.Sub(time.UnixMilli(created)))
		if p.Purpose == PurposePasskey {
			p.Kind = "passkey"
		} else {
			var list []string
			_ = fromJSON(redirects, &list)
			p.ClientName = CleanName(name)
			p.Host, p.Kind, _ = describeClient(list)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DenyAllPending refuses every waiting request at once (a flood, or a request the owner does not recognize) and says
// how many there were.
func (a *Auth) DenyAllPending(ctx context.Context, by string) (int64, error) {
	now := a.now()
	res, err := a.db.ExecContext(ctx,
		"update approvals set state = 'denied', decided_by = ?, decided = ? where state = 'pending' and expires > ?",
		cleanLine(by, 100), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Decide approves or denies a pending code; by says who (never the code). It is only ever once: a decided, spent or
// expired code is ErrNoApproval.
func (a *Auth) Decide(ctx context.Context, code string, allow bool, by string) (ApprovalView, error) {
	v, err := a.ShowApproval(ctx, code)
	if err != nil {
		return ApprovalView{}, err
	}
	norm, _ := normalizeCode(code)
	state := "denied"
	if allow {
		state = "approved"
	}
	now := a.now()
	res, err := a.db.ExecContext(ctx,
		"update approvals set state = ?, decided_by = ?, decided = ? where hash = ? and state = 'pending' and expires > ?",
		state, cleanLine(by, 100), now.UnixMilli(), hash(norm), now.UnixMilli())
	if err != nil {
		return ApprovalView{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ApprovalView{}, ErrNoApproval
	}
	v.State = state
	return v, nil
}

// PollApproval is what the requesting browser asks with its waiter secret: pending; approved, with where to send the
// browser (a code was issued just now, once); denied, with where to send it; or expired for everything else.
func (a *Auth) PollApproval(ctx context.Context, waiter string) (state, redirect string, err error) {
	now := a.now()
	var purpose, params, client, by string
	err = a.db.QueryRowContext(ctx,
		"select state, purpose, params, client, coalesce(decided_by, '') from approvals where waiter = ? and expires > ?",
		hash(waiter), now.UnixMilli()).Scan(&state, &purpose, &params, &client, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return "expired", "", nil
	}
	if err != nil {
		return "", "", err
	}
	switch state {
	case "pending":
		return "pending", "", nil
	case "approved", "denied":
	default:
		return "expired", "", nil
	}
	if purpose == PurposePasskey && state == "approved" {
		// the passkey flow takes the approval itself (setup); nothing is spent here
		return "approved", "", nil
	}
	// spent before anything is issued, so two polls at once cannot both get a code
	res, err := a.db.ExecContext(ctx, "update approvals set state = 'done' where waiter = ? and state = ?", hash(waiter), state)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "expired", "", nil
	}
	if purpose == PurposePasskey {
		return state, "", nil
	}
	var stored map[string]string
	if err := fromJSON(params, &stored); err != nil {
		return "", "", err
	}
	p := url.Values{}
	for k, v := range stored {
		p.Set(k, v)
	}
	if state == "denied" {
		redirect, err = a.Denied(p)
	} else {
		redirect, err = a.IssueCode(ctx, p)
		// the core's own record of who let a client in (the code is not in it); the CLI is another process
		if c, cerr := a.client(ctx, client); err == nil && cerr == nil && c != nil {
			slog.Info("identity: client approved", "client", c.ID, "name", c.Name, "by", by)
		}
	}
	return state, redirect, err
}

// CancelApproval is the requesting browser saying no (its Cancel button): the request is denied, and its next poll
// sends the browser back with access_denied. A request already decided stays as it was.
func (a *Auth) CancelApproval(ctx context.Context, waiter string) error {
	now := a.now()
	_, err := a.db.ExecContext(ctx,
		"update approvals set state = 'denied', decided_by = 'browser', decided = ? where waiter = ? and state = 'pending' and expires > ?",
		now.UnixMilli(), hash(waiter), now.UnixMilli())
	return err
}
