//go:build linux

package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
)

func resumeSession(sha string) guidedState {
	return guidedState{ContinuationSHA: sha, Download: guidedDownload{Build: buildinfo.CurrentBuild(), Commit: buildinfo.Current()}}
}

func withImages(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := continuationImages
	continuationImages = dir
	t.Cleanup(func() { continuationImages = old })
	return dir
}

func saveImage(t *testing.T, dir string, data []byte) string {
	t.Helper()
	sha := digestSetupBytes(data)
	path := filepath.Join(dir, sha, "hypermesh-deploy")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResumeRunsInPlaceWhenThisFileIsTheVerifiedCopy(t *testing.T) {
	withImages(t)
	running, err := runningContinuation()
	if err != nil {
		t.Fatal(err)
	}
	target, err := resumeTarget(resumeSession(digestSetupBytes(running)))
	if err != nil || target != "" {
		t.Fatalf("matching file: target=%q err=%v", target, err)
	}
}

func TestResumeHandsOverToTheVerifiedCopyWhenThisFileDiffers(t *testing.T) {
	dir := withImages(t)
	image := saveImage(t, dir, []byte("the verified installer"))
	target, err := resumeTarget(resumeSession(digestSetupBytes([]byte("the verified installer"))))
	if err != nil || target != image {
		t.Fatalf("target=%q err=%v want %q", target, err, image)
	}
}

func TestResumeRefusesWithAFixWhenTheVerifiedCopyIsMissingOrChanged(t *testing.T) {
	dir := withImages(t)
	good := []byte("the verified installer")
	sha := digestSetupBytes(good)
	cases := map[string]func(){
		"missing": func() {},
		"changed": func() {
			path := saveImage(t, dir, good)
			if err := os.WriteFile(path, []byte("replaced"), 0700); err != nil {
				t.Fatal(err)
			}
		},
		"loose mode": func() {
			path := saveImage(t, dir, good)
			if err := os.Chmod(path, 0777); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			os.RemoveAll(filepath.Join(dir, sha))
			setup()
			target, err := resumeTarget(resumeSession(sha))
			if err == nil || target != "" {
				t.Fatalf("target=%q err=%v", target, err)
			}
			for _, want := range []string{"does not match the verified copy", "Nothing was changed", "sudo /usr/local/bin/hypermesh-host", "website verifier"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q in: %v", want, err)
				}
			}
		})
	}
}

func TestResumeLeavesUnapprovedAndDifferentBuildSessionsToTheirOwnPaths(t *testing.T) {
	withImages(t)
	for name, s := range map[string]guidedState{
		"not approved yet": resumeSession(""),
		"newer build":      {ContinuationSHA: strings.Repeat("a", 64), Download: guidedDownload{Build: buildinfo.CurrentBuild() + 1, Commit: buildinfo.Current()}},
		"other commit":     {ContinuationSHA: strings.Repeat("a", 64), Download: guidedDownload{Build: buildinfo.CurrentBuild(), Commit: "0000000000000000000000000000000000000000"}},
	} {
		if target, err := resumeTarget(s); err != nil || target != "" {
			t.Fatalf("%s: target=%q err=%v", name, target, err)
		}
	}
}

func TestVerifiedCopyReceivesTheOriginalOptionsAndItsStatusIsKept(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "copy")
	body := "#!/bin/sh\necho \"args: $*\"\ncat >/dev/null\nexit 7\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	code := runVerifiedCopy(context.Background(), script, []string{"--yes", "--network", "test"}, strings.NewReader(""), &out, &diagnostic)
	if code == 1 && strings.Contains(diagnostic.String(), "could not run") {
		t.Skip("the temporary directory cannot run programs")
	}
	if code != 7 || !strings.Contains(out.String(), "args: install --yes --network test") {
		t.Fatalf("code=%d out=%q diag=%q", code, out.String(), diagnostic.String())
	}
	if code = runVerifiedCopy(context.Background(), filepath.Join(dir, "absent"), nil, strings.NewReader(""), &out, &diagnostic); code == 0 {
		t.Fatal("a missing copy reported success")
	}
}

func TestRunningVerifiedBytesRepairAChangedOrUnsafeImage(t *testing.T) {
	good := []byte("the verified installer")
	for name, damage := range map[string]func(path string){
		"changed bytes": func(path string) { os.WriteFile(path, []byte("replaced"), 0700) },
		"loose mode":    func(path string) { os.Chmod(path, 0777) },
		"symlink": func(path string) {
			os.Remove(path)
			os.Symlink("/etc/passwd", path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := withImages(t)
			path := saveImage(t, dir, good)
			damage(path)
			payload := agentPayload{agentFile{path, digestSetupBytes(good), 0700}, good}
			if err := ensureContinuationImage(path, payload); err != nil {
				t.Fatal(err)
			}
			if ok, err := agentFileMatches(path, payload); err != nil || !ok {
				t.Fatalf("not restored: %v %v", ok, err)
			}
			if target, err := resumeTarget(resumeSession(digestSetupBytes(good))); err != nil || target != path {
				t.Fatalf("restored copy not used: %q %v", target, err)
			}
		})
	}
	dir := withImages(t)
	payload := agentPayload{agentFile{filepath.Join(dir, "x"), digestSetupBytes(good), 0700}, good}
	if err := os.MkdirAll(payload.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureContinuationImage(payload.Path, payload); err == nil {
		t.Fatal("a directory was removed")
	}
}
