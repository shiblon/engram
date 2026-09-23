package engram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTopicInjectIncludesMonitorContractAndShortIndex(t *testing.T) {
	text := InjectContextText(InjectResult{}, InjectResult{Topics: []Topic{{
		Name: "graph-memory", Purpose: "share OOM findings", RetireWhen: "the investigation closes",
	}}}, 5)
	for _, want := range []string{
		"## Pubsub topics", "engram topic --help", "engram topic monitor <topic>",
		"background process", "retain its handle across turns", "Keep it running for the whole session",
		"Post useful findings as they arise", "without waiting for another user prompt",
		"graph-memory", "share OOM findings", "retire when",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("topic inject missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "message body") {
		t.Errorf("topic inject contains message bodies: %q", text)
	}
}

func mustCreateTopic(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	if err := CreateTopic(ctx, db, Topic{
		Name: name, Purpose: "share graph memory findings", RetireWhen: "the investigation closes",
	}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
}

func TestTopicPostReadAndRetire(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	mustCreateTopic(t, ctx, db, "graph-memory")

	first, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "first finding", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "second finding", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Message.TS <= first.Message.TS {
		t.Fatalf("SQLite timestamps are not strictly increasing: first=%d second=%d",
			first.Message.TS, second.Message.TS)
	}

	after := first.Message.TS
	third, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "third finding", &after)
	if err != nil {
		t.Fatal(err)
	}
	if third.Unseen == nil || len(third.Unseen.Messages) != 1 || third.Unseen.Messages[0].Body != "second finding" {
		t.Fatalf("post catch-up = %+v, want only the second finding", third.Unseen)
	}

	read, err := ReadSubtopic(ctx, db, "graph-memory", "allocator", &after)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Messages) != 2 || read.Messages[0].Body != "second finding" || read.Messages[1].Body != "third finding" {
		t.Fatalf("read after first = %+v, want second and third", read.Messages)
	}

	subtopics, err := ListSubtopics(ctx, db, "graph-memory")
	if err != nil {
		t.Fatal(err)
	}
	if len(subtopics) != 1 || subtopics[0].Name != "allocator" {
		t.Fatalf("subtopics = %+v, want allocator", subtopics)
	}

	retired, err := RetireTopic(ctx, db, "graph-memory")
	if err != nil || !retired {
		t.Fatalf("retire = %v, %v", retired, err)
	}
	if _, err := ListSubtopics(ctx, db, "graph-memory"); !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("list after retirement error = %v, want ErrTopicNotFound", err)
	}
}

func TestTopicBoundsRejectUnboundedGrowth(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	for i := 0; i < MaxActiveTopics; i++ {
		mustCreateTopic(t, ctx, db, fmt.Sprintf("topic-%d", i))
	}
	if err := CreateTopic(ctx, db, Topic{
		Name: "one-too-many", Purpose: "exceed the cap", RetireWhen: "never",
	}); err == nil || !strings.Contains(err.Error(), "topic limit") {
		t.Fatalf("create beyond topic cap error = %v", err)
	}

	body := strings.Repeat("x", MaxTopicMessageChars)
	for i := 0; i < TopicTailHardChars/MaxTopicMessageChars; i++ {
		result, err := PostTopicMessage(ctx, db, "topic-0", "bounded", body, nil)
		if err != nil {
			t.Fatalf("post %d within tail cap: %v", i, err)
		}
		if i >= TopicTailSoftChars/MaxTopicMessageChars-1 && !result.CompactionRecommended {
			t.Fatalf("post %d did not recommend compaction at the soft cap", i)
		}
	}
	if _, err := PostTopicMessage(ctx, db, "topic-0", "bounded", "overflow", nil); err == nil ||
		!strings.Contains(err.Error(), "compact") {
		t.Fatalf("post beyond tail cap error = %v", err)
	}

	for i := 0; i < MaxActiveSubtopics-1; i++ { // "bounded" already occupies one slot.
		if _, err := PostTopicMessage(ctx, db, "topic-0", fmt.Sprintf("subtopic-%d", i), "finding", nil); err != nil {
			t.Fatalf("create subtopic %d within cap: %v", i, err)
		}
	}
	if _, err := PostTopicMessage(ctx, db, "topic-0", "one-too-many", "finding", nil); err == nil ||
		!strings.Contains(err.Error(), "subtopic limit") {
		t.Fatalf("post beyond subtopic cap error = %v", err)
	}
}

func TestTopicCompactionStagesImmutablePrefix(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	mustCreateTopic(t, ctx, db, "graph-memory")

	first, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := BeginTopicCompaction(ctx, db, "graph-memory", "allocator")
	if err != nil {
		t.Fatal(err)
	}
	if stage.CutoffTS != second.Message.TS || len(stage.Messages) != 2 {
		t.Fatalf("stage = %+v, want first two messages through second", stage)
	}
	if _, err := BeginTopicCompaction(ctx, db, "graph-memory", "allocator"); err == nil {
		t.Fatal("second compactor acquired an already staged subtopic")
	} else {
		var busy *CompactionInProgressError
		if !errors.As(err, &busy) {
			t.Fatalf("second begin error = %v, want CompactionInProgressError", err)
		}
	}

	third, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "third arrived during compaction", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyTopicCompaction(ctx, db, stage.Token, "first and second established the baseline")
	if err != nil {
		t.Fatal(err)
	}
	if result.Through != second.Message.TS || result.Generation != 1 {
		t.Fatalf("compaction result = %+v", result)
	}

	after := first.Message.TS
	read, err := ReadSubtopic(ctx, db, "graph-memory", "allocator", &after)
	if err != nil {
		t.Fatal(err)
	}
	if !read.HistoryCompacted || read.Checkpoint == "" {
		t.Fatalf("read did not report compacted history: %+v", read)
	}
	if len(read.Messages) != 1 || read.Messages[0].TS != third.Message.TS {
		t.Fatalf("tail after compaction = %+v, want only concurrent third post", read.Messages)
	}
	if _, err := ApplyTopicCompaction(ctx, db, stage.Token, "duplicate"); !errors.Is(err, ErrCompactionStageNotFound) {
		t.Fatalf("reapplying stage error = %v, want not found", err)
	}
}

func TestTopicCompactionRejectsAndClearsStaleStage(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	mustCreateTopic(t, ctx, db, "graph-memory")
	if _, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "finding", nil); err != nil {
		t.Fatal(err)
	}
	stage, err := BeginTopicCompaction(ctx, db, "graph-memory", "allocator")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, db, `UPDATE topic_subtopics SET checkpoint_generation = checkpoint_generation + 1`)
	if _, err := ApplyTopicCompaction(ctx, db, stage.Token, "stale checkpoint"); !errors.Is(err, ErrCompactionStageStale) {
		t.Fatalf("apply stale stage error = %v, want ErrCompactionStageStale", err)
	}
	var stages int
	mustScan(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_compaction_stages`), &stages)
	if stages != 0 {
		t.Fatalf("stale stage rows = %d, want 0", stages)
	}
	var messages int
	mustScan(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_messages`), &messages)
	if messages != 1 {
		t.Fatalf("messages after stale apply = %d, want source message preserved", messages)
	}
}

func TestConcurrentTopicCompactorsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "topics.db")
	db1 := mustOpen(t, ctx, path)
	defer db1.Close()
	db2 := mustOpen(t, ctx, path)
	defer db2.Close()
	mustCreateTopic(t, ctx, db1, "graph-memory")
	if _, err := PostTopicMessage(ctx, db1, "graph-memory", "allocator", "finding", nil); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{db1, db2} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			_, err := BeginTopicCompaction(ctx, db, "graph-memory", "allocator")
			errs <- err
		}(db)
	}
	close(start)
	wg.Wait()
	close(errs)

	var winners, guarded int
	for err := range errs {
		if err == nil {
			winners++
			continue
		}
		var busy *CompactionInProgressError
		if errors.As(err, &busy) {
			guarded++
			continue
		}
		t.Fatalf("unexpected concurrent begin error: %v", err)
	}
	if winners != 1 || guarded != 1 {
		t.Fatalf("concurrent compaction results: winners=%d guarded=%d, want 1 and 1", winners, guarded)
	}
}

func TestCheckTopicReturnsCurrentHeadsWithoutWaiting(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	mustCreateTopic(t, ctx, db, "graph-memory")

	first, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "first finding", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PostTopicMessage(ctx, db, "graph-memory", "allocator", "second finding", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := PostTopicMessage(ctx, db, "graph-memory", "retention", "other finding", nil)
	if err != nil {
		t.Fatal(err)
	}

	events, err := CheckTopic(ctx, db, "graph-memory", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("current head events = %+v, want two subtopics", events)
	}
	heads := make(map[string]int64, len(events))
	for _, event := range events {
		heads[event.Subtopic] = event.TS
	}
	if heads["allocator"] != second.Message.TS || heads["retention"] != other.Message.TS {
		t.Fatalf("current head events = %+v, want latest allocator and retention heads", events)
	}

	after := first.Message.TS
	events, err = CheckTopic(ctx, db, "graph-memory", "allocator", &after)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Subtopic != "allocator" || events[0].TS != second.Message.TS {
		t.Fatalf("allocator events after first = %+v, want second head", events)
	}

	after = max(second.Message.TS, other.Message.TS)
	events, err = CheckTopic(ctx, db, "graph-memory", "", &after)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events after latest head = %+v, want none", events)
	}
}

func TestMonitorTopicStaysAliveAcrossMessages(t *testing.T) {
	ctx, timeout := context.WithTimeout(context.Background(), 2*time.Second)
	defer timeout()
	monitorCtx, stop := context.WithCancel(ctx)
	path := filepath.Join(t.TempDir(), "monitor.db")
	reader := mustOpen(t, monitorCtx, path)
	defer reader.Close()
	writer := mustOpen(t, ctx, path)
	defer writer.Close()
	mustCreateTopic(t, ctx, writer, "graph-memory")

	events := make(chan TopicEvent, 2)
	errs := make(chan error, 1)
	go func() {
		errs <- MonitorTopic(monitorCtx, reader, "graph-memory", "allocator", TopicMonitorOptions{
			PollInterval: 5 * time.Millisecond,
		}, func(event TopicEvent) error {
			events <- event
			return nil
		})
	}()
	for i, body := range []string{"first finding", "second finding"} {
		time.Sleep(20 * time.Millisecond)
		post, err := PostTopicMessage(ctx, writer, "graph-memory", "allocator", body, nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-events:
			if event.Event != "message" || event.Subtopic != "allocator" || event.TS != post.Message.TS {
				t.Fatalf("event %d = %+v, want allocator message at %d", i, event, post.Message.TS)
			}
		case err := <-errs:
			t.Fatalf("monitor stopped after %d messages: %v", i, err)
		case <-ctx.Done():
			t.Fatal("monitor did not emit before timeout")
		}
	}
	select {
	case err := <-errs:
		t.Fatalf("monitor returned while its topic and session remained active: %v", err)
	default:
	}
	stop()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("monitor cancellation error = %v, want context.Canceled", err)
	}
}
