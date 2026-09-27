package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanRunRecognizesLinkedWorktree(t *testing.T) {
	main, worktree, common := linkedWorktreeFixture(t)
	root, configPath, _ := applicationFixture(t)
	plan, err := PlanRun(context.Background(), runRequest(configPath, worktree, ""), PlanOptions{
		InvocationDir: root,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		Environment:   HostEnvironment{Home: filepath.Join(root, "home"), TempDir: root},
		Runner:        &fakeGit{root: worktree},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Project.RootDir != worktree || plan.LinkedWorktree == nil || plan.LinkedWorktree.commonDir != common {
		t.Fatalf("unexpected worktree plan: root=%q metadata=%#v (main %q)", plan.Project.RootDir, plan.LinkedWorktree, main)
	}
}

func linkedWorktreeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	worktree := filepath.Join(base, "feature")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", main)
	if err := os.WriteFile(filepath.Join(main, "example"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", main, "add", "example")
	git("-C", main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "first")
	git("-C", main, "worktree", "add", "-b", "feature", worktree)
	return main, worktree, git("-C", worktree, "rev-parse", "--git-common-dir")
}

func TestPlanLinkedWorktreeAndInvocationMounts(t *testing.T) {
	main, worktree, common := linkedWorktreeFixture(t)
	ordinary, err := planLinkedWorktree(main)
	if err != nil || ordinary != nil {
		t.Fatalf("main worktree: %#v, %v", ordinary, err)
	}
	linked, err := planLinkedWorktree(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if linked == nil || linked.commonDir != common {
		t.Fatalf("linked worktree: %#v, want common dir %q", linked, common)
	}
	original, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	mounts, err := linked.mounts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 3 || mounts[0].Source != common || mounts[0].Target != gitMetadataTarget || mounts[0].ReadOnly ||
		mounts[1].Target != projectTarget+"/.git" || !mounts[1].ReadOnly ||
		mounts[2].Target != gitMetadataTarget+"/worktrees/feature/gitdir" || !mounts[2].ReadOnly {
		t.Fatalf("unexpected Git mounts: %#v", mounts)
	}
	pointer, err := os.ReadFile(mounts[1].Source)
	if err != nil {
		t.Fatal(err)
	}
	if string(pointer) != "gitdir: /run/wisp/git/worktrees/feature\n" {
		t.Fatalf("container pointer = %q", pointer)
	}
	backlink, err := os.ReadFile(mounts[2].Source)
	if err != nil {
		t.Fatal(err)
	}
	if string(backlink) != "/workspace/current/.git\n" {
		t.Fatalf("container backlink = %q", backlink)
	}
	after, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil || string(after) != string(original) {
		t.Fatalf("host pointer changed: %q, %v", after, err)
	}
}

func TestPlanLinkedWorktreeRejectsInvalidMetadata(t *testing.T) {
	_, worktree, common := linkedWorktreeFixture(t)
	admin := filepath.Join(common, "worktrees", "feature")
	for _, tc := range []struct {
		name, file, contents string
	}{
		{"absolute commondir", filepath.Join(admin, "commondir"), common + "\n"},
		{"wrong backlink", filepath.Join(admin, "gitdir"), "/other/.git\n"},
		{"bad gitfile", filepath.Join(worktree, ".git"), "gitdir: /missing\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(tc.file, original, 0600) })
			if err := os.WriteFile(tc.file, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := planLinkedWorktree(worktree); err == nil {
				t.Fatal("expected invalid worktree metadata to be rejected")
			}
			if err := os.WriteFile(tc.file, original, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}
