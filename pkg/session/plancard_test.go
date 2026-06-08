package session

import (
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/session/store"
)

// TestPlanCardPublisher_PublishesOnUpdate covers the happy path: the
// publisher writes a single "plan/active" card whose slug comes from
// metadata.progress_metric and whose detail contains the title and
// every entry's content.
func TestPlanCardPublisher_PublishesOnUpdate(t *testing.T) {
	s := store.NewObservableStore("")
	pub := planCardPublisher(s)

	pub(
		"Wire feature X",
		[]agent.PlanEntry{
			{Content: "design", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
			{Content: "wire api", Status: agent.PlanEntryStatusInProgress, Priority: agent.PlanEntryPriorityHigh},
			{Content: "tests", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityMedium},
		},
		map[string]string{"progress_metric": "endpoints migrated 1/3"},
		"tc-publish",
	)

	cards := s.List()
	if len(cards) != 1 {
		t.Fatalf("expected 1 plan card, got %d: %#v", len(cards), cards)
	}
	c := cards[0]
	if c.Source != "plan" || c.Key != "active" {
		t.Errorf("card address = (%s,%s); want (plan,active)", c.Source, c.Key)
	}
	if c.Slug != "endpoints migrated 1/3" {
		t.Errorf("slug = %q; want progress_metric value", c.Slug)
	}
	if c.EntryID != "tc-publish" {
		t.Errorf("entry_id = %q; want tc-publish", c.EntryID)
	}
	if !strings.Contains(c.Detail, "Wire feature X") {
		t.Errorf("detail missing title:\n%s", c.Detail)
	}
	for _, frag := range []string{"design", "wire api", "tests"} {
		if !strings.Contains(c.Detail, frag) {
			t.Errorf("detail missing %q:\n%s", frag, c.Detail)
		}
	}
	if strings.Contains(c.Detail, "progress_metric") {
		t.Errorf("detail unexpectedly contains metadata key:\n%s", c.Detail)
	}
}

// TestPlanCardPublisher_SlugFallback verifies the "<done>/<total> <status>"
// fallback when no progress_metric is set.
func TestPlanCardPublisher_SlugFallback(t *testing.T) {
	s := store.NewObservableStore("")
	pub := planCardPublisher(s)

	pub("",
		[]agent.PlanEntry{
			{Content: "a", Status: agent.PlanEntryStatusCompleted},
			{Content: "b", Status: agent.PlanEntryStatusInProgress},
			{Content: "c", Status: agent.PlanEntryStatusPending},
		},
		nil, "tc",
	)

	cards := s.List()
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].Slug != "1/3 in_progress" {
		t.Errorf("slug = %q; want '1/3 in_progress'", cards[0].Slug)
	}
}

func TestPlanCardPublisher_SlugDone(t *testing.T) {
	s := store.NewObservableStore("")
	pub := planCardPublisher(s)

	pub("",
		[]agent.PlanEntry{
			{Content: "a", Status: agent.PlanEntryStatusCompleted},
			{Content: "b", Status: agent.PlanEntryStatusCompleted},
		},
		nil, "tc",
	)
	cards := s.List()
	if len(cards) != 1 || cards[0].Slug != "2/2 done" {
		t.Fatalf("slug = %q; want '2/2 done'", cards[0].Slug)
	}
}

// TestPlanCardPublisher_ClearsOnEmpty covers the design rule that
// passing an empty entries list clears the card.
func TestPlanCardPublisher_ClearsOnEmpty(t *testing.T) {
	s := store.NewObservableStore("")
	pub := planCardPublisher(s)

	pub("",
		[]agent.PlanEntry{
			{Content: "a", Status: agent.PlanEntryStatusPending},
		},
		nil, "tc1",
	)
	if len(s.List()) != 1 {
		t.Fatal("expected card after first publish")
	}

	pub("", nil, nil, "tc2")
	if got := s.List(); len(got) != 0 {
		t.Errorf("expected card cleared, got %#v", got)
	}
}

// TestPlanCardPublisher_NilStoreIsSafe pins that the publisher still
// works when the host has no observable store — ObservableStore.Put
// and Clear are documented nil-safe at the store layer.
func TestPlanCardPublisher_NilStoreIsSafe(t *testing.T) {
	pub := planCardPublisher(nil)
	pub("",
		[]agent.PlanEntry{
			{Content: "a", Status: agent.PlanEntryStatusPending},
		},
		nil, "tc",
	)
	pub("", nil, nil, "tc-clear")
}
