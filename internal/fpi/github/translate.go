// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GitHub payload shapes (the subset Ore reads).

type ghRepository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// envelope is what every webhook payload carries.
type envelope struct {
	Action       string        `json:"action"`
	Repository   *ghRepository `json:"repository"`
	Sender       ghUser        `json:"sender"`
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

func (e envelope) owner() string {
	if e.Repository == nil {
		return ""
	}
	if e.Repository.Owner.Login != "" {
		return e.Repository.Owner.Login
	}
	o, _, _ := strings.Cut(e.Repository.FullName, "/")
	return o
}

func (e envelope) name() string {
	if e.Repository == nil {
		return ""
	}
	if e.Repository.Name != "" {
		return e.Repository.Name
	}
	_, n, _ := strings.Cut(e.Repository.FullName, "/")
	return n
}

type pushPayload struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Created bool   `json:"created"`
	Deleted bool   `json:"deleted"`
	Forced  bool   `json:"forced"`
	Compare string `json:"compare"`
	Pusher  struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"pusher"`
	HeadCommit *struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
}

type ghIssue struct {
	ID          int64     `json:"id"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason *string   `json:"state_reason"`
	HTMLURL     string    `json:"html_url"`
	User        ghUser    `json:"user"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

type issuesPayload struct {
	Action string  `json:"action"`
	Issue  ghIssue `json:"issue"`
}

type ghPRSide struct {
	Label string `json:"label"`
	Ref   string `json:"ref"`
	SHA   string `json:"sha"`
}

type ghPullRequest struct {
	ID             int64    `json:"id"`
	Number         int      `json:"number"`
	State          string   `json:"state"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	HTMLURL        string   `json:"html_url"`
	Draft          bool     `json:"draft"`
	Merged         bool     `json:"merged"`
	MergeCommitSHA *string  `json:"merge_commit_sha"`
	User           ghUser   `json:"user"`
	Head           ghPRSide `json:"head"`
	Base           ghPRSide `json:"base"`
}

type pullRequestPayload struct {
	Action      string        `json:"action"`
	Number      int           `json:"number"`
	PullRequest ghPullRequest `json:"pull_request"`
}

type pingPayload struct {
	Zen    string `json:"zen"`
	HookID int64  `json:"hook_id"`
	Hook   struct {
		Type   string   `json:"type"`
		ID     int64    `json:"id"`
		Events []string `json:"events"`
		Config struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"config"`
	} `json:"hook"`
}

// Translated events: what Ore acts on, independent of the wire shape.

// PushEvent is a push webhook.
type PushEvent struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Created bool   `json:"created"`
	Deleted bool   `json:"deleted"`
	Forced  bool   `json:"forced"`
	Pusher  string `json:"pusher"`
	Compare string `json:"compare,omitempty"`
}

// IssueEvent is an issues webhook.
type IssueEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	StateReason string `json:"state_reason,omitempty"`
	Author      string `json:"author"`
	URL         string `json:"url"`
}

// PullRequestEvent is a pull_request webhook.
type PullRequestEvent struct {
	Action         string `json:"action"`
	Number         int    `json:"number"`
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
	HeadRef        string `json:"head_ref"`
	HeadSHA        string `json:"head_sha"`
	BaseRef        string `json:"base_ref"`
	BaseSHA        string `json:"base_sha"`
	Author         string `json:"author"`
	URL            string `json:"url"`
}

// PingEvent is the ping GitHub sends when a webhook is created.
type PingEvent struct {
	Zen    string   `json:"zen"`
	HookID int64    `json:"hook_id"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// IgnoredEvent is an event (or action) Ore acknowledges without acting on.
type IgnoredEvent struct {
	Event  string `json:"event"`
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func parseEnvelope(body []byte) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return env, fmt.Errorf("payload is not JSON: %w", err)
	}
	return env, nil
}

// Translate decodes a webhook body for the X-GitHub-Event into a typed
// event: PushEvent, IssueEvent, PullRequestEvent, PingEvent or IgnoredEvent.
func Translate(event string, body []byte) (any, error) {
	switch event {
	case "push":
		var p pushPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("push payload: %w", err)
		}
		if p.Ref == "" {
			return nil, fmt.Errorf("push payload: missing ref")
		}
		return PushEvent{Ref: p.Ref, Before: p.Before, After: p.After, Created: p.Created, Deleted: p.Deleted, Forced: p.Forced, Pusher: p.Pusher.Name, Compare: p.Compare}, nil
	case "issues":
		var p issuesPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("issues payload: %w", err)
		}
		if p.Issue.Number == 0 {
			return nil, fmt.Errorf("issues payload: missing issue number")
		}
		switch p.Action {
		case "opened", "edited", "closed", "reopened":
		default:
			return IgnoredEvent{Event: event, Action: p.Action, Reason: "action not mirrored"}, nil
		}
		ev := IssueEvent{Action: p.Action, Number: p.Issue.Number, ID: p.Issue.ID, Title: p.Issue.Title, Body: p.Issue.Body, State: p.Issue.State, Author: p.Issue.User.Login, URL: p.Issue.HTMLURL}
		if p.Issue.StateReason != nil {
			ev.StateReason = *p.Issue.StateReason
		}
		return ev, nil
	case "pull_request":
		var p pullRequestPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("pull_request payload: %w", err)
		}
		n := p.Number
		if n == 0 {
			n = p.PullRequest.Number
		}
		if n == 0 {
			return nil, fmt.Errorf("pull_request payload: missing number")
		}
		switch p.Action {
		case "opened", "reopened", "synchronize", "edited", "closed", "ready_for_review":
		default:
			return IgnoredEvent{Event: event, Action: p.Action, Reason: "action not mirrored"}, nil
		}
		pr := p.PullRequest
		ev := PullRequestEvent{Action: p.Action, Number: n, ID: pr.ID, Title: pr.Title, Body: pr.Body, State: pr.State, Draft: pr.Draft, Merged: pr.Merged,
			HeadRef: pr.Head.Ref, HeadSHA: pr.Head.SHA, BaseRef: pr.Base.Ref, BaseSHA: pr.Base.SHA, Author: pr.User.Login, URL: pr.HTMLURL}
		if pr.MergeCommitSHA != nil {
			ev.MergeCommitSHA = *pr.MergeCommitSHA
		}
		return ev, nil
	case "ping":
		var p pingPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("ping payload: %w", err)
		}
		return PingEvent{Zen: p.Zen, HookID: p.HookID, URL: p.Hook.Config.URL, Events: p.Hook.Events}, nil
	}
	var env envelope
	_ = json.Unmarshal(body, &env)
	return IgnoredEvent{Event: event, Action: env.Action, Reason: "event not handled"}, nil
}
