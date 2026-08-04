package bot

import (
	"testing"

	"shop_bot/internal/config"
)

func TestResolveAdminTargets_GroupWithTopic(t *testing.T) {
	cfg := &config.Config{
		AdminIDs:             []int64{111, 222},
		AdminGroupID:         -1001234567890,
		TopicOrdersNew:       5,
		TopicOrdersPaid:      7,
		TopicOrdersDelivered: 9,
	}

	groupID, threadID, adminIDs := resolveAdminTargets(cfg, AdminEventOrderPaid)
	if groupID != -1001234567890 {
		t.Fatalf("groupID = %d, want -1001234567890", groupID)
	}
	if threadID != 7 {
		t.Fatalf("threadID = %d, want 7", threadID)
	}
	if len(adminIDs) != 0 {
		t.Fatalf("adminIDs = %v, want none (group configured)", adminIDs)
	}
}

func TestResolveAdminTargets_GroupWithoutTopic(t *testing.T) {
	cfg := &config.Config{
		AdminIDs:     []int64{111},
		AdminGroupID: -1001234567890,
		// No TOPIC_* configured — message goes to the General topic.
	}

	groupID, threadID, adminIDs := resolveAdminTargets(cfg, AdminEventOrderNew)
	if groupID != -1001234567890 {
		t.Fatalf("groupID = %d, want -1001234567890", groupID)
	}
	if threadID != 0 {
		t.Fatalf("threadID = %d, want 0 (no topic)", threadID)
	}
	if len(adminIDs) != 0 {
		t.Fatalf("adminIDs = %v, want none (group configured)", adminIDs)
	}
}

func TestResolveAdminTargets_NoGroupFallsBackToDMs(t *testing.T) {
	cfg := &config.Config{
		AdminIDs: []int64{111, 222},
		// AdminGroupID unset, topics irrelevant even if configured.
		TopicOrdersDelivered: 9,
	}

	groupID, threadID, adminIDs := resolveAdminTargets(cfg, AdminEventOrderDelivered)
	if groupID != 0 {
		t.Fatalf("groupID = %d, want 0 (no group)", groupID)
	}
	if threadID != 0 {
		t.Fatalf("threadID = %d, want 0 (no group)", threadID)
	}
	if len(adminIDs) != 2 || adminIDs[0] != 111 || adminIDs[1] != 222 {
		t.Fatalf("adminIDs = %v, want [111 222]", adminIDs)
	}
}

func TestTopicFor_MapsEachEventToItsTopic(t *testing.T) {
	cfg := &config.Config{
		TopicOrdersNew:       1,
		TopicOrdersPaid:      2,
		TopicOrdersDelivered: 3,
	}

	cases := []struct {
		kind AdminEvent
		want int
	}{
		{AdminEventOrderNew, 1},
		{AdminEventOrderPaid, 2},
		{AdminEventOrderDelivered, 3},
	}
	for _, tc := range cases {
		if got := topicFor(cfg, tc.kind); got != tc.want {
			t.Errorf("topicFor(kind=%d) = %d, want %d", tc.kind, got, tc.want)
		}
	}
}
