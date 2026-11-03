//go:build linux

package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestHeadlessApprovalIsShownOnceAndDoesNotSelfApprove(t *testing.T) {
	_, journal := registrationFixture(t)
	original := journal
	remote := &fakeRegistrationRemote{states: []string{"pending", "pending", "approved", "completed"}, deviceID: "12345678-1234-4234-8234-123456789aaa"}
	var diagnostic bytes.Buffer
	waits, publishes := 0, 0
	err := finishRegistration(context.Background(), &journal, remote,
		func(registrationJournal) error { return nil },
		func(registrationJournal) error { publishes++; return nil },
		&diagnostic, func(context.Context, time.Duration) error {
			waits++
			if remote.completes != 0 {
				t.Fatal("completed while approval was still pending")
			}
			return nil
		})
	if err != nil || waits != 2 || remote.completes != 1 || publishes != 1 || journal.State != "registered-inactive" {
		t.Fatalf("approval flow changed: err=%v waits=%d completes=%d publishes=%d state=%s", err, waits, remote.completes, publishes, journal.State)
	}
	if strings.Count(diagnostic.String(), "Host approval URL: ") != 1 || !strings.Contains(diagnostic.String(), journal.Request.RequestID) {
		t.Fatal("pending approval URL missing or repeated")
	}
	for _, secret := range []string{original.PrivateKey, original.DeviceSecret} {
		if secret != "" && strings.Contains(diagnostic.String(), secret) {
			t.Fatal("private registration material appeared in diagnostics")
		}
	}
}

func TestHeadlessApprovalDisplayFailurePreservesPendingJournal(t *testing.T) {
	_, journal := registrationFixture(t)
	original := journal
	remote := &fakeRegistrationRemote{states: []string{"pending"}}
	saves := 0
	err := finishRegistration(context.Background(), &journal, remote,
		func(registrationJournal) error { saves++; return nil },
		func(registrationJournal) error { t.Fatal("published before approval"); return nil },
		failedApprovalDisplay{}, func(context.Context, time.Duration) error {
			t.Fatal("polled after the approval URL could not be displayed")
			return nil
		})
	if err == nil || err.Error() != "cannot display host approval instructions" || saves != 1 ||
		journal.State != "pending" || journal.Request != original.Request ||
		journal.PrivateKey != original.PrivateKey || journal.DeviceSecret != original.DeviceSecret ||
		remote.gets != 0 || remote.completes != 0 {
		t.Fatalf("display failure changed recovery authority: err=%v saves=%d", err, saves)
	}
}
