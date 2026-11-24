package offsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"kedalio/core/db"
)

// DefaultKeep is how many copies the store keeps: a month of daily ones. The local folder is cheap and holds two weeks;
// the store is the one that survives a lost disk, so it goes back further.
const DefaultKeep = 30

// DefaultPrefix is the folder of the bucket the copies go in: deleting beyond Keep never reaches outside it.
const DefaultPrefix = "kedalio-core/"

// Uploader keeps the store's copies equal to the newest Keep of the local ones.
type Uploader struct {
	Client *Client
	Keep   int

	mu     sync.Mutex
	lastOK time.Time
	failed time.Time // when the current run of failures began; zero while the last attempt was good
	newest time.Time // the time of the newest copy the store was seen to hold
}

// State is what the owner can read off the health route: when the last upload went through, and since when it has been
// failing. A zero LastOK with no failure is a core that has not tried yet.
type State struct {
	LastOK       time.Time
	FailingSince time.Time
	// NewestCopy is the time of the newest copy the store holds. LastOK alone says nothing of the copies: a sync with
	// nothing to send goes through as well.
	NewestCopy time.Time
}

func (u *Uploader) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	return State{LastOK: u.lastOK, FailingSince: u.failed, NewestCopy: u.newest}
}

// Sync uploads the copies in dir the store lacks and then removes the store's oldest beyond Keep, counting only files
// named like copies under the prefix: whatever else the owner keeps there stays. Deleting comes after every upload went
// through, so a failing store never costs a copy. It gives the number uploaded.
func (u *Uploader) Sync(ctx context.Context, dir string) (uploaded int, err error) {
	defer func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		switch {
		case err == nil:
			u.lastOK, u.failed = time.Now(), time.Time{}
		case u.failed.IsZero():
			u.failed = time.Now()
		}
	}()
	local, err := db.Copies(dir)
	if err != nil {
		return 0, err
	}
	remote, err := u.Client.List(ctx)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	haveTag := map[string]bool{}
	var all []string
	for _, o := range remote {
		if copyName, ok := strings.CutSuffix(o.Name, ".tag"); ok {
			haveTag[copyName] = true
		}
		if _, ok := db.CopyTime(o.Name); ok {
			have[o.Name] = true
			all = append(all, o.Name)
			u.sawCopy(o.Name)
		}
	}
	paths := map[string]string{}
	for _, p := range local {
		name := filepath.Base(p)
		paths[name] = p
		if !have[name] {
			all = append(all, name)
		}
	}
	// the stamp is fixed-width UTC, so names sort by time
	slices.Sort(all)
	if len(all) > u.Keep {
		all = all[len(all)-u.Keep:]
	}
	for _, name := range all {
		// the tag goes first: a copy that reaches the store always has its tag, never the other way round
		if p, ok := paths[name]; ok && !haveTag[name] {
			if _, err := os.Stat(db.TagPath(p)); err == nil {
				if err := u.Client.Put(ctx, name+".tag", db.TagPath(p)); err != nil {
					return uploaded, err
				}
			}
		}
		if have[name] {
			continue
		}
		if err := u.Client.Put(ctx, name, paths[name]); err != nil {
			return uploaded, err
		}
		uploaded++
		u.sawCopy(name)
	}
	for _, o := range remote {
		// a tag goes with its copy; one whose copy is newer than what is kept stays, it is how a missing copy shows
		name, isTag := strings.CutSuffix(o.Name, ".tag")
		if _, ok := db.CopyTime(name); !ok || slices.Contains(all, name) || (isTag && !(len(all) > 0 && name < all[0])) {
			continue
		}
		if err := u.Client.Delete(ctx, o.Name); err != nil {
			return uploaded, err
		}
	}
	return uploaded, nil
}

func (u *Uploader) sawCopy(name string) {
	t, ok := db.CopyTime(name)
	if !ok {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if t.After(u.newest) {
		u.newest = t
	}
}

// RestoreOptions is what Restore needs to trust a copy, and what the owner may waive.
type RestoreOptions struct {
	// Key is the database key (from the recovery code): the tags are signed under a key derived from it.
	Key []byte
	// AllowUnsigned accepts a copy with no tag, a copy made before tags existed.
	AllowUnsigned bool
	// AllowOlder accepts a copy older than the newest one the store holds a valid tag for.
	AllowOlder bool
	// Warn is told what the owner waived; nil says nothing.
	Warn func(msg string)
}

// maxTag is the most a tag may weigh: a real one is a few hundred bytes.
const maxTag = 64 << 10

// Restore downloads a copy (the newest when name is empty) into dataDir as core.db, ready for kedalio-core to start on:
// it is still encrypted, so the core starts locked and the recovery code opens it. It refuses a folder that has a
// database already, a download whose size is not the store's, and a copy whose tag does not verify (altered, cut, renamed
// or of another instance). A copy older than the newest verified tag in the store is a rollback and is refused too. The
// two waivers are the owner's to give: an unsigned copy (opts.AllowUnsigned) and an older one (opts.AllowOlder).
func Restore(ctx context.Context, c *Client, name, dataDir string, opts RestoreOptions) (string, error) {
	target := filepath.Join(dataDir, "core.db")
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("%s exists already: restore into an empty data folder", target)
	}
	if opts.Key == nil {
		return "", errors.New("the recovery code is needed to check the copy's tag")
	}
	warn := opts.Warn
	if warn == nil {
		warn = func(string) {}
	}
	objects, err := c.List(ctx)
	if err != nil {
		return "", err
	}
	var want *Object
	tags := map[string]bool{}
	for i := len(objects) - 1; i >= 0; i-- {
		if copyName, ok := strings.CutSuffix(objects[i].Name, ".tag"); ok {
			tags[copyName] = true
		}
		if _, ok := db.CopyTime(objects[i].Name); ok && want == nil && (name == "" || objects[i].Name == name) {
			want = &objects[i]
		}
	}
	if want == nil {
		return "", errors.New("the store has no such copy")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	partial := target + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	n, err := c.GetUpTo(ctx, want.Name, f, want.Size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != want.Size {
		err = fmt.Errorf("download %s: %d bytes came, the store lists %d", want.Name, n, want.Size)
	}
	if err == nil {
		err = verify(ctx, c, want.Name, partial, objects, tags, opts, warn)
	}
	if err != nil {
		os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, target); err != nil {
		os.Remove(partial)
		return "", err
	}
	return want.Name, nil
}

// fetchTag is the tag stored for a copy, nil when the store has none.
func fetchTag(ctx context.Context, c *Client, name string, tags map[string]bool) (*db.Tag, error) {
	if !tags[name] {
		return nil, nil
	}
	var b limitedBuffer
	if _, err := c.Get(ctx, name+".tag", &b); err != nil {
		return nil, err
	}
	t, err := db.ParseTag(b.Bytes())
	return &t, err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxTag {
		return 0, errors.New("a tag that large is not one")
	}
	return b.Buffer.Write(p)
}

func verify(ctx context.Context, c *Client, name, path string, objects []Object, tags map[string]bool, opts RestoreOptions, warn func(string)) error {
	tag, err := fetchTag(ctx, c, name, tags)
	if err != nil {
		return fmt.Errorf("refusing %s: %w", name, err)
	}
	switch err := db.VerifyCopy(path, name, tag, opts.Key); {
	case errors.Is(err, db.ErrUnsigned):
		if !opts.AllowUnsigned {
			return fmt.Errorf("%s has no tag, so nothing shows it is the owner's and unaltered; restore it only if you trust the store, with -allow-unsigned", name)
		}
		warn(name + " has no tag: it was not checked and may have been altered or swapped")
	case err != nil:
		return fmt.Errorf("refusing %s: %w", name, err)
	}
	// rollback: the newest copy the store holds a valid tag for, found from the newest tag down
	for i := len(objects) - 1; i >= 0; i-- {
		copyName, ok := strings.CutSuffix(objects[i].Name, ".tag")
		if !ok || copyName <= name {
			continue
		}
		t, err := fetchTag(ctx, c, copyName, tags)
		if err != nil || t == nil || db.CheckTag(*t, opts.Key) != nil {
			continue
		}
		// the signed time must be the one in the name, as VerifyCopy demands: a genuine old tag copied under a far-future
		// name would otherwise make every restore look like a rollback
		signed, terr := t.Time()
		named, ok := db.CopyTime(copyName)
		if terr != nil || !ok || !signed.Equal(named) {
			continue
		}
		if opts.AllowOlder {
			warn(name + " is older than " + copyName + ", which the store holds with a valid tag: a rollback if the newer one is the owner's")
			return nil
		}
		return fmt.Errorf("refusing %s: the store holds a newer copy with a valid tag, %s (an older copy may be a rollback); "+
			"restore the newest, or pass -allow-older", name, copyName)
	}
	return nil
}
