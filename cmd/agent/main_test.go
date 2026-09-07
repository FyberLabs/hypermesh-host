package main

import "testing"

func TestSplitOwnerArgs(t *testing.T) {
	cases := []struct {
		in      []string
		wantOK  bool
		wantPre []string
		want    []string
	}{
		{in: []string{"owner", "sell-pause"}, wantOK: true, wantPre: []string{}, want: []string{"sell-pause"}},
		{in: []string{"-state-dir=./state", "owner", "lease-stop", "--lease-id=abc"}, wantOK: true, wantPre: []string{"-state-dir=./state"}, want: []string{"lease-stop", "--lease-id=abc"}},
		{in: []string{"-state-dir", "./state", "owner", "sell-pause", "--until=2026-09-08T00:00:00Z"}, wantOK: true, wantPre: []string{"-state-dir", "./state"}, want: []string{"sell-pause", "--until=2026-09-08T00:00:00Z"}},
		{in: []string{"-control-plane-url", "https://api.test.hyperme.sh"}, wantOK: false},
		{in: []string{"--", "owner", "sell-pause"}, wantOK: false},
	}
	for _, tc := range cases {
		pre, rest, ok := splitOwnerArgs(tc.in)
		if ok != tc.wantOK {
			t.Fatalf("%v ok=%v want %v", tc.in, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if !stringSlicesEqual(pre, tc.wantPre) || !stringSlicesEqual(rest, tc.want) {
			t.Fatalf("%v pre=%v rest=%v", tc.in, pre, rest)
		}
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
