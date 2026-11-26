package mcp

import (
	"context"
	"strings"

	"kedalio/protocol"
)

// A cloud client is an OAuth client registered with a redirect off this machine: claude.ai's connector (its chats,
// Projects, cloud Claude Code sessions), ChatGPT's. It is the owner talking, behind the owner's passkey at consent, but
// where no local hook guards what it writes and a model may follow what it read in a page or a mail. So it reads
// everything, works on the tasks, and leaves in the memory only inbox entries that wait for the owner: the nightly
// upkeep does not place them (inboxEntry.cloud).

type cloudKey struct{}

// isCloud is whether the request came from a cloud client (set by authorized, from Config.Client).
func isCloud(ctx context.Context) bool {
	c, _ := ctx.Value(cloudKey{}).(bool)
	return c
}

// cloudMay are the tools a cloud client can call, and the only ones its tools/list shows: everything that reads, the
// tasks' save, the memory's write (whose ops cloudWrite limits). Deleting, the sources, the upkeep, the links and the
// settings' writes stay the owner's, on a local client.
var cloudMay = map[string]bool{
	protocol.ToolMemoryRecall: true, protocol.ToolMemoryRead: true, protocol.ToolMemoryList: true,
	protocol.ToolMemoryWrite: true, protocol.ToolTasksBrief: true, protocol.ToolTasksList: true,
	protocol.ToolTasksGet: true, protocol.ToolTasksSave: true, protocol.ToolSessionStart: true,
	protocol.ToolSettingsGet: true,
}

const cloudMayList = "memory_recall, memory_read, memory_list, memory_write (remember, and append to the inbox), tasks_brief, tasks_list, tasks_get, tasks_save, session_start, settings_get"

// cloudWhyPrefix starts the why line of an inbox entry a cloud client left: the mark the upkeep and session_start
// read. Nothing else writes a why that starts so: the other reasons are remember's, in its own words.
const cloudWhyPrefix = "cloud: "

// cloudWhy is the why line of a cloud client's inbox entry: who left it, and that it is the owner's to place.
func (s *Server) cloudWhy(ctx context.Context) string {
	// a token keeps its marker: an OAuth client and a token may both be named alice
	from := strings.TrimPrefix(s.by(ctx), "oauth:")
	if name, ok := strings.CutPrefix(from, namedAuthorPrefix); ok {
		from = "token " + name
	}
	return cloudWhyPrefix + "from " + from + ", waits for the owner"
}

// isCloudWhy is whether the text of a why line (after «why:») is a cloud client's mark.
func isCloudWhy(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), strings.TrimSpace(cloudWhyPrefix))
}

// cloudWrite is what memory_write allows a cloud client before the op runs: remember (which lands in the inbox, never on
// a page) and append to the inbox. A line of its own that reads «why: …» is refused: an entry keeps one why line, and
// one the client wrote would push out the server's mark.
func (s *Server) cloudWrite(_ context.Context, a protocol.MemoryWrite) error {
	switch {
	case a.Op == "remember":
		return nil
	case a.Op == "append" && a.Where == "inbox":
		for i, l := range strings.Split(strings.TrimSpace(a.Text), "\n") {
			if i > 0 && whyLine.MatchString(strings.TrimSpace(l)) {
				return refusal("text", "a line starting «why:» is the server's", "write the reason in the entry's own words")
			}
		}
		return nil
	case a.Op == "append":
		return refusal("where", "a cloud client appends only to the inbox, where the owner places what it said",
			"pass where inbox, or keep the fact with op remember")
	}
	return refusal("op", a.Op+" is not for a cloud client: what it keeps in the memory waits in the inbox for the owner",
		"use op remember, or op append with where inbox")
}

// cloudTask is what tasks_save allows a cloud client: anything but a repeat, which makes a task come back by itself,
// and worked_by, which hands a task to an agent or a run.
func cloudTask(q protocol.TasksSave) error {
	if q.Repeat != nil {
		return refusal("repeat", "a cloud client does not make tasks repeat", "leave repeat out; the owner sets it from a local client")
	}
	if q.WorkedBy != nil {
		return refusal("worked_by", "a cloud client does not assign work to agents or runs", "leave worked_by out")
	}
	return nil
}
