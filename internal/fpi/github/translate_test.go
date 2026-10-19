// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"reflect"
	"testing"
)

// TestTranslateFixtures translates every recorded payload under testdata.
func TestTranslateFixtures(t *testing.T) {
	cases := []struct {
		fixture, event string
		want           any
	}{
		{"ping.json", "ping", PingEvent{Zen: "Keep it logically awesome.", HookID: 512345678, URL: "https://ore.example.org/hooks/github", Events: []string{"issues", "pull_request", "push"}}},
		{"push.json", "push", PushEvent{Ref: "refs/heads/main", Before: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", After: "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567", Pusher: "octocat", Compare: "https://github.com/octo/app/compare/6113728f27ae...0a1b2c3d4e5f"}},
		{"push_deleted.json", "push", PushEvent{Ref: "refs/heads/feature/retry", Before: "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432", After: "0000000000000000000000000000000000000000", Deleted: true, Pusher: "octocat", Compare: "https://github.com/octo/app/compare/9f8e7d6c5b4a...000000000000"}},
		{"push_forced.json", "push", PushEvent{Ref: "refs/heads/feature/retry", Before: "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432", After: "aa11bb22cc33dd44ee55ff6677889900aabbccdd", Forced: true, Pusher: "octocat", Compare: "https://github.com/octo/app/compare/9f8e7d6c5b4a...aa11bb22cc33"}},
		{"issues_opened.json", "issues", IssueEvent{Action: "opened", Number: 42, ID: 2765432109, Title: "Fix the flaky retry in the uploader", Body: "The uploader retries once and gives up.\n\nSteps: run `go test ./uploader -count=20`; about one run in five fails with `context deadline exceeded`.", State: "open", Author: "octocat", URL: "https://github.com/octo/app/issues/42"}},
		{"issues_edited.json", "issues", IssueEvent{Action: "edited", Number: 42, ID: 2765432109, Title: "Fix the flaky retry in the uploader (timeouts)", Body: "The uploader retries once and gives up. The timeout is 100 ms, the server answers in 150 ms under load.", State: "open", Author: "octocat", URL: "https://github.com/octo/app/issues/42"}},
		{"issues_closed.json", "issues", IssueEvent{Action: "closed", Number: 42, ID: 2765432109, Title: "Fix the flaky retry in the uploader (timeouts)", Body: "The uploader retries once and gives up. The timeout is 100 ms, the server answers in 150 ms under load.", State: "closed", StateReason: "completed", Author: "octocat", URL: "https://github.com/octo/app/issues/42"}},
		{"issues_labeled.json", "issues", IgnoredEvent{Event: "issues", Action: "labeled", Reason: "action not mirrored"}},
		{"pull_request_opened.json", "pull_request", PullRequestEvent{Action: "opened", Number: 7, ID: 2345678901, Title: "Retry uploads with exponential backoff", Body: "Fixes #42.\n\nThe uploader now retries up to five times with jittered backoff.", State: "open", HeadRef: "feature/retry", HeadSHA: "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432", BaseRef: "main", BaseSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", Author: "octocat", URL: "https://github.com/octo/app/pull/7"}},
		{"pull_request_synchronize.json", "pull_request", PullRequestEvent{Action: "synchronize", Number: 7, ID: 2345678901, Title: "Retry uploads with exponential backoff", Body: "Fixes #42.\n\nThe uploader now retries up to five times with jittered backoff.", State: "open", MergeCommitSHA: "c0ffee0000000000000000000000000000000001", HeadRef: "feature/retry", HeadSHA: "aa11bb22cc33dd44ee55ff6677889900aabbccdd", BaseRef: "main", BaseSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", Author: "octocat", URL: "https://github.com/octo/app/pull/7"}},
		{"pull_request_closed.json", "pull_request", PullRequestEvent{Action: "closed", Number: 7, ID: 2345678901, Title: "Retry uploads with exponential backoff", Body: "Fixes #42.\n\nThe uploader now retries up to five times with jittered backoff.", State: "closed", Merged: true, MergeCommitSHA: "c0ffee0000000000000000000000000000000002", HeadRef: "feature/retry", HeadSHA: "aa11bb22cc33dd44ee55ff6677889900aabbccdd", BaseRef: "main", BaseSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", Author: "octocat", URL: "https://github.com/octo/app/pull/7"}},
		{"star.json", "star", IgnoredEvent{Event: "star", Action: "created", Reason: "event not handled"}},
		{"installation_created.json", "installation", IgnoredEvent{Event: "installation", Action: "created", Reason: "event not handled"}},
	}
	for _, c := range cases {
		got, err := Translate(c.event, fixture(t, c.fixture))
		if err != nil {
			t.Errorf("%s: %v", c.fixture, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.fixture, got, c.want)
		}
	}
}

func TestTranslateRejectsBrokenPayloads(t *testing.T) {
	for _, c := range []struct{ event, body string }{
		{"push", `{"repository":{"full_name":"octo/app"}}`}, // no ref
		{"issues", `{"action":"opened","issue":{}}`},        // no number
		{"pull_request", `{"action":"opened"}`},             // no number
		{"push", `not json`},
	} {
		if _, err := Translate(c.event, []byte(c.body)); err == nil {
			t.Errorf("%s %q: expected an error", c.event, c.body)
		}
	}
}

func TestEnvelopeFallsBackToFullName(t *testing.T) {
	env, err := parseEnvelope([]byte(`{"repository":{"full_name":"Octo/App"}}`))
	if err != nil || env.owner() != "Octo" || env.name() != "App" {
		t.Fatalf("%+v %v", env, err)
	}
	env, _ = parseEnvelope([]byte(`{"action":"created"}`))
	if env.owner() != "" || env.name() != "" {
		t.Fatalf("expected no repository, got %q/%q", env.owner(), env.name())
	}
}
