package models

// PendingReply is a reply the harness built and has not posted: the turn number and the full body, marker
// included.
type PendingReply struct {
	Turn int
	Body string
}

// HarnessState is what the harness keeps across restarts of one session: the pending reply, if any, whether the
// workspace is cloned and set up, and whether a push of the issue branch still waits for its merge request.
type HarnessState struct {
	Pending             *PendingReply
	Started             bool
	MergeRequestPending bool
}
