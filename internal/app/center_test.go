//go:build client && !server

package app

import "testing"

func TestShouldOpenTab(t *testing.T) {
	cases := []struct {
		name          string
		opts          CenterOptions
		activeClients int
		claim         bool
		want          bool
		wantClaimed   bool
	}{
		{name: "no request, no tab", opts: CenterOptions{}, want: false},
		{name: "a session start wins the claim", opts: CenterOptions{Open: true}, claim: true, want: true, wantClaimed: true},
		{name: "a session start that loses the claim opens nothing", opts: CenterOptions{Open: true}, claim: false, want: false, wantClaimed: true},
		{name: "a tab is already streaming", opts: CenterOptions{Open: true}, activeClients: 1, claim: true, want: false},
		// `center open`: the user typed it, so neither a tab on another desktop
		// nor another terminal's cooldown may swallow the request.
		{name: "forced past a streaming tab", opts: CenterOptions{Open: true, Force: true}, activeClients: 3, want: true},
		{name: "forced past a lost claim", opts: CenterOptions{Open: true, Force: true}, claim: false, want: true},
		{name: "force without open still opens nothing", opts: CenterOptions{Force: true}, want: false},
	}
	for _, tc := range cases {
		claimed := false
		claim := func() bool { claimed = true; return tc.claim }
		if got := shouldOpenTab(tc.opts, tc.activeClients, claim); got != tc.want {
			t.Errorf("%s: shouldOpenTab = %t, want %t", tc.name, got, tc.want)
		}
		if claimed != tc.wantClaimed {
			t.Errorf("%s: claim called = %t, want %t", tc.name, claimed, tc.wantClaimed)
		}
	}
}
