package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// origin is a bare repo plus the working clone used to push new commits to it.
type origin struct {
	url  string // bare repo, clone source
	work string // working clone, used to advance branches
}

// gitInSoft runs a git command in dir and returns trimmed output, or "" on
// error. Used where a non-zero exit is a meaningful answer.
func gitInSoft(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = testGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// newOrigin creates a bare origin repo with branches "main" and "other", both
// holding one commit. No network is involved.
func newOrigin(t *testing.T) *origin {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")

	gitIn(t, root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	gitIn(t, root, "init", "--quiet", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "--quiet", "-m", "one")
	gitIn(t, work, "remote", "add", "origin", bare)
	gitIn(t, work, "push", "--quiet", "origin", "main")

	// A second branch, so a poisoned FETCH_HEAD has more than one candidate.
	gitIn(t, work, "checkout", "--quiet", "-b", "other")
	if err := os.WriteFile(filepath.Join(work, "g"), []byte("other\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "--quiet", "-m", "other")
	gitIn(t, work, "push", "--quiet", "origin", "other")
	gitIn(t, work, "checkout", "--quiet", "main")

	return &origin{url: bare, work: work}
}

// advance adds a commit to branch on the origin's working clone and pushes it,
// so a clone has something to fast-forward to.
func (o *origin) advance(t *testing.T, branch, content string) {
	t.Helper()
	gitIn(t, o.work, "checkout", "--quiet", branch)
	if err := os.WriteFile(filepath.Join(o.work, "f-"+content), []byte(content+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, o.work, "add", ".")
	gitIn(t, o.work, "commit", "--quiet", "-m", content)
	gitIn(t, o.work, "push", "--quiet", "origin", branch)
}

// TestPullFastForward is the happy path: origin moved ahead, Pull catches up.
func TestPullFastForward(t *testing.T) {
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	before := gitIn(t, clone, "rev-parse", "HEAD")

	o.advance(t, "main", "two")

	if err := Pull(clone); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	after := gitIn(t, clone, "rev-parse", "HEAD")
	if after == before {
		t.Fatalf("Pull did not advance HEAD (still %s)", before)
	}
	want := gitIn(t, clone, "rev-parse", "origin/main")
	if after != want {
		t.Fatalf("HEAD = %s, want origin/main %s", after, want)
	}
	if got := gitIn(t, clone, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("Pull left HEAD on %q, want main", got)
	}
}

// TestPullMultipleMergeCandidates is the deterministic regression for the
// field error "fatal: Cannot fast-forward to multiple branches".
//
// "git pull" merges every FETCH_HEAD entry that is not marked "not-for-merge".
// Whenever more than one branch is a merge candidate — here forced via a
// multi-valued branch.<name>.merge, in the field via a concurrent fetch
// clobbering FETCH_HEAD mid-pull — "git pull --ff-only" dies. Pull must not
// consult FETCH_HEAD at all.
func TestPullMultipleMergeCandidates(t *testing.T) {
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	gitIn(t, clone, "config", "--add", "branch.main.merge", "refs/heads/other")
	o.advance(t, "main", "two")

	if err := Pull(clone); err != nil {
		t.Fatalf("Pull with multiple merge candidates: %v", err)
	}
	head := gitIn(t, clone, "rev-parse", "HEAD")
	want := gitIn(t, clone, "rev-parse", "origin/main")
	if head != want {
		t.Fatalf("HEAD = %s, want origin/main %s", head, want)
	}
}

// TestPullIgnoresPoisonedFetchHead asserts Pull never reads FETCH_HEAD: a
// stale one listing several branches as merge candidates must not influence
// what gets merged, nor which branch HEAD ends up on.
func TestPullIgnoresPoisonedFetchHead(t *testing.T) {
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	o.advance(t, "main", "two")
	o.advance(t, "other", "three")

	// An explicit wildcard refspec on the command line marks every branch as
	// a merge candidate (no "not-for-merge" marker).
	gitIn(t, clone, "fetch", "--quiet", "origin", "+refs/heads/*:refs/remotes/origin/*")
	poisoned, err := os.ReadFile(filepath.Join(clone, ".git", "FETCH_HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	var forMerge int
	for _, line := range strings.Split(strings.TrimSpace(string(poisoned)), "\n") {
		if line != "" && !strings.Contains(line, "not-for-merge") {
			forMerge++
		}
	}
	if forMerge < 2 {
		t.Fatalf("test setup: FETCH_HEAD has %d merge candidates, want >= 2:\n%s", forMerge, poisoned)
	}

	if err := Pull(clone); err != nil {
		t.Fatalf("Pull with poisoned FETCH_HEAD: %v", err)
	}
	if got := gitIn(t, clone, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("Pull left HEAD on %q, want main", got)
	}
	head := gitIn(t, clone, "rev-parse", "HEAD")
	if want := gitIn(t, clone, "rev-parse", "origin/main"); head != want {
		t.Fatalf("HEAD = %s, want origin/main %s", head, want)
	}
	// "other" was fetched and is a merge candidate in FETCH_HEAD; it must not
	// have been merged.
	if other := gitIn(t, clone, "rev-parse", "origin/other"); head == other {
		t.Fatalf("Pull merged origin/other (%s) — it read FETCH_HEAD", other)
	}
}

// TestPullDetachedHead covers a package pinned to a tag or sha: there is no
// branch to fast-forward, so Pull refreshes refs and returns nil, leaving the
// pinned HEAD exactly where it was.
func TestPullDetachedHead(t *testing.T) {
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	gitIn(t, clone, "checkout", "--quiet", "--detach", "HEAD")
	pinned := gitIn(t, clone, "rev-parse", "HEAD")

	o.advance(t, "main", "two")

	if err := Pull(clone); err != nil {
		t.Fatalf("Pull on detached HEAD: %v", err)
	}
	if got := gitIn(t, clone, "rev-parse", "HEAD"); got != pinned {
		t.Fatalf("Pull moved a pinned HEAD: %s -> %s", pinned, got)
	}
	if branch := gitInSoft(clone, "symbolic-ref", "--quiet", "--short", "HEAD"); branch != "" {
		t.Fatalf("HEAD unexpectedly attached to %q", branch)
	}
	// The refresh must still have happened, so a later CheckoutRef resolves.
	remote := gitIn(t, clone, "rev-parse", "origin/main")
	upstream := gitIn(t, o.work, "rev-parse", "main")
	if remote != upstream {
		t.Fatalf("origin/main = %s, want %s — Pull did not refresh refs", remote, upstream)
	}
}

// TestPullConcurrentFetch is the field scenario: fir's own Fetch running on the
// same clone while an update pulls. Every Pull must succeed.
func TestPullConcurrentFetch(t *testing.T) {
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = Fetch(clone)
			}
		}
	}()

	for i := 0; i < 8; i++ {
		o.advance(t, "main", "c"+string(rune('a'+i)))
		if err := Pull(clone); err != nil {
			close(stop)
			<-done
			t.Fatalf("Pull #%d raced with Fetch: %v", i, err)
		}
	}
	close(stop)
	<-done
}

// TestPullNoUpstreamConfigured covers the upstreamRef fallback: a clone whose
// branch has no branch.<name>.merge / .remote config (so "@{u}" fails) must
// still fast-forward via origin/<branch>, which the default refspec produces.
func TestPullNoUpstreamConfigured(t *testing.T) {
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	gitIn(t, clone, "config", "--unset-all", "branch.main.merge")
	gitIn(t, clone, "config", "--unset-all", "branch.main.remote")
	if u := gitInSoft(clone, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); u != "" {
		t.Fatalf("test setup: @{u} still resolves to %q", u)
	}
	o.advance(t, "main", "two")

	if err := Pull(clone); err != nil {
		t.Fatalf("Pull without upstream config: %v", err)
	}
	head := gitIn(t, clone, "rev-parse", "HEAD")
	if want := gitIn(t, clone, "rev-parse", "origin/main"); head != want {
		t.Fatalf("HEAD = %s, want origin/main %s", head, want)
	}
}
