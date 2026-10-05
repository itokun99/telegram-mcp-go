package events

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/itokun99/telegram-mcp-go/internal/kit"
)

// burst is the per-chat pending record events.py keeps in _pending_msgs: the
// window of a client's message run plus the identifiers of its first and last
// message.
type burst struct {
	firstTS  float64
	lastTS   float64
	count    int
	firstID  int64
	lastID   int64
	name     string
	username *string
}

// pendingChat is one entry of wait_for_new_message's pending_chats list. It is
// a non-consuming view: the record stays pending for wait_for_settled_message.
type pendingChat struct {
	ChatID        int64   `json:"chat_id"`
	Name          string  `json:"name"`
	Username      *string `json:"username"`
	Count         int     `json:"count"`
	LastMessageID int64   `json:"last_message_id"`
}

// burstSummary is the settled-burst payload shared by
// wait_for_settled_message and the feed lines.
type burstSummary struct {
	Event          bool    `json:"event"`
	ChatID         int64   `json:"chat_id"`
	Name           string  `json:"name"`
	Username       *string `json:"username"`
	MessageCount   int     `json:"message_count"`
	FirstMessageID int64   `json:"first_message_id"`
	LastMessageID  int64   `json:"last_message_id"`
	BurstSeconds   float64 `json:"burst_seconds"`
}

// feedLine is one appended JSONL record: the burst summary without the "event"
// marker, plus the wall-clock time the burst settled.
type feedLine struct {
	ChatID         int64   `json:"chat_id"`
	Name           string  `json:"name"`
	Username       *string `json:"username"`
	MessageCount   int     `json:"message_count"`
	FirstMessageID int64   `json:"first_message_id"`
	LastMessageID  int64   `json:"last_message_id"`
	BurstSeconds   float64 `json:"burst_seconds"`
	TS             float64 `json:"ts"`
}

// tracker holds the pending bursts and the feed consumer. It replaces the
// Python module's _pending_msgs dict plus its asyncio.Event, which is a
// buffered one-slot latch here: clearActivity drops any pending token,
// signalActivity posts one without blocking.
type tracker struct {
	mu       sync.Mutex
	pending  map[int64]*burst
	order    []int64
	activity chan struct{}

	feedRunning   bool
	feedSettleMS  int
	autostartUsed bool
	feedCancel    context.CancelFunc
	feedDone      chan struct{}

	mono func() float64
	wall func() time.Time
	rep  kit.Reporter
}

func newTracker(deps Deps) *tracker {
	return &tracker{
		pending:      map[int64]*burst{},
		activity:     make(chan struct{}, 1),
		feedSettleMS: defaultSettleMS,
		mono:         deps.Monotonic,
		wall:         deps.Wall,
		rep:          deps.Reporter,
	}
}

// clearActivity drops a pending activity token.
func (t *tracker) clearActivity() {
	select {
	case <-t.activity:
	default:
	}
}

// signalActivity wakes one waiter. A full latch already holds a token, so a
// second message cannot block or lose a wake-up.
func (t *tracker) signalActivity() {
	select {
	case t.activity <- struct{}{}:
	default:
	}
}

// waitActivity blocks for at most timeout, returning false on timeout or
// cancellation. Every waiter clears the latch before it evaluates its
// condition, so a message that arrives during the check leaves a token the
// following wait consumes immediately instead of being lost.
func (t *tracker) waitActivity(ctx context.Context, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-t.activity:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// waitIdle blocks until activity or cancellation, with no deadline.
func (t *tracker) waitIdle(ctx context.Context) bool {
	select {
	case <-t.activity:
		return true
	case <-ctx.Done():
		return false
	}
}

// record folds one incoming message into its chat's burst and wakes waiters.
func (t *tracker) record(chatID, messageID int64, name string, username *string, now float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rec, ok := t.pending[chatID]
	if !ok {
		t.pending[chatID] = &burst{
			firstTS:  now,
			lastTS:   now,
			count:    1,
			firstID:  messageID,
			lastID:   messageID,
			name:     name,
			username: username,
		}
		t.order = append(t.order, chatID)
	} else {
		// Handlers for one chat can interleave across the sender lookup, so
		// identifiers may arrive out of order: keep min/max rather than
		// trusting arrival order.
		rec.lastTS = math.Max(rec.lastTS, now)
		rec.firstID = min(rec.firstID, messageID)
		rec.lastID = max(rec.lastID, messageID)
		rec.count++
	}
	t.signalActivity()
}

// popLocked removes a chat's burst. The caller holds t.mu.
func (t *tracker) popLocked(chatID int64) *burst {
	rec, ok := t.pending[chatID]
	if !ok {
		return nil
	}
	delete(t.pending, chatID)
	for i, id := range t.order {
		if id == chatID {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	return rec
}

// take pops a settled burst, mirroring wait_for_settled_message's
// _pending_msgs.pop: the record is consumed so the next call returns the next
// settled chat.
func (t *tracker) take(chatID int64) *burst {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.popLocked(chatID)
}

// pendingChats returns the non-consuming pending view in arrival order,
// filtered by target (nil = any chat) and the allowlist.
func (t *tracker) pendingChats(target *int64, allowed func(int64) bool) []pendingChat {
	t.mu.Lock()
	defer t.mu.Unlock()

	chats := make([]pendingChat, 0, len(t.order))
	for _, chatID := range t.order {
		rec, ok := t.pending[chatID]
		if !ok {
			continue
		}
		if target != nil && chatID != *target {
			continue
		}
		if allowed != nil && !allowed(chatID) {
			continue
		}
		chats = append(chats, pendingChat{
			ChatID:        chatID,
			Name:          kit.SanitizeName(rec.name),
			Username:      rec.username,
			Count:         rec.count,
			LastMessageID: rec.lastID,
		})
	}
	return chats
}

// scanSettled finds a chat whose burst has been quiet for settle seconds.
// With target set every other chat is ignored: waiting for one person must not
// be interrupted by unrelated conversations. It returns the settled chat ID and
// the seconds until the soonest pending chat settles, or a negative remainder
// when nothing is pending.
func (t *tracker) scanSettled(now, settle float64, target *int64, allowed func(int64) bool) (int64, float64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scanSettledLocked(now, settle, target, allowed)
}

func (t *tracker) scanSettledLocked(now, settle float64, target *int64, allowed func(int64) bool) (int64, float64, bool) {
	soonest := -1.0
	for _, chatID := range t.order {
		rec, ok := t.pending[chatID]
		if !ok {
			continue
		}
		if target != nil && chatID != *target {
			continue
		}
		if allowed != nil && !allowed(chatID) {
			continue
		}
		quiet := now - rec.lastTS
		if quiet >= settle {
			return chatID, 0, true
		}
		if remaining := settle - quiet; soonest < 0 || remaining < soonest {
			soonest = remaining
		}
	}
	return 0, soonest, false
}

// appendFeedLine writes one burst to the feed and consumes it, holding t.mu
// across the write so no other consumer can observe the burst between the pop
// and the append. A write failure leaves the burst pending, so the same burst
// is retried on the next pass instead of being dropped. It reports false when
// the burst is no longer settled.
func (t *tracker) appendFeedLine(chatID int64, settle float64, deps Deps, allowed func(int64) bool) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rec, ok := t.pending[chatID]
	if !ok {
		return false, nil
	}
	now := t.mono()
	if now-rec.lastTS < settle {
		return false, nil
	}
	line := feedLine{
		ChatID:         chatID,
		Name:           kit.SanitizeName(rec.name),
		Username:       rec.username,
		MessageCount:   rec.count,
		FirstMessageID: rec.firstID,
		LastMessageID:  rec.lastID,
		BurstSeconds:   round2(rec.lastTS - rec.firstTS),
		TS:             round2(wallSeconds(t.wall())),
	}
	if err := writeFeedLine(deps, line); err != nil {
		return false, err
	}
	t.popLocked(chatID)
	return true, nil
}

// feedStatus reports (enabled, settle_ms, autostart consumed).
func (t *tracker) feedStatus(_ Deps) (bool, int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.feedRunning, t.feedSettleMS, t.autostartUsed
}

// maybeAutostart starts the feed on the first incoming event when
// TELEGRAM_EVENT_FEED is truthy. It is one-shot: an explicit enable or disable
// consumes the autostart, so a feed the user turned off is never resurrected by
// the next message.
func (t *tracker) maybeAutostart(deps Deps) {
	if !deps.EventFeed {
		return
	}
	t.mu.Lock()
	if t.autostartUsed || t.feedRunning {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	if err := t.startFeed(deps, t.currentSettleMS()); err != nil {
		t.rep.Error("Cannot create event feed file")
	}
}

func (t *tracker) currentSettleMS() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.feedSettleMS
}

// startFeed validates the feed file and starts the consumer goroutine. Any
// explicit or implicit start consumes the env autostart.
func (t *tracker) startFeed(deps Deps, settleMS int) error {
	t.mu.Lock()
	alreadyRunning := t.feedRunning
	sameSettle := t.feedSettleMS == settleMS
	t.mu.Unlock()

	// Validate before starting, so a bad path (missing directory, read-only
	// mount) fails cleanly with no orphan consumer.
	if err := touchFeedFile(deps); err != nil {
		return err
	}
	if alreadyRunning && sameSettle {
		return nil
	}
	t.stopFeed()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	t.mu.Lock()
	t.feedRunning = true
	t.feedSettleMS = settleMS
	t.autostartUsed = true
	t.feedCancel = cancel
	t.feedDone = done
	t.mu.Unlock()

	go func() {
		defer close(done)
		t.runFeed(ctx, deps, float64(settleMS)/1000)
	}()
	return nil
}

// stopFeed cancels the consumer and waits for it to exit, so the feed is
// observably off before the caller reports it disabled.
func (t *tracker) stopFeed() {
	t.mu.Lock()
	cancel, done := t.feedCancel, t.feedDone
	t.feedCancel, t.feedDone = nil, nil
	t.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done

	t.mu.Lock()
	t.feedRunning = false
	t.mu.Unlock()
}

// runFeed consumes settled bursts and appends them to the feed file.
func (t *tracker) runFeed(ctx context.Context, deps Deps, settle float64) {
	for {
		// Clear before scanning: a message that lands during the scan leaves a
		// token the wait below consumes instead of being missed.
		t.clearActivity()

		chatID, soonest, settled := t.scanSettled(t.mono(), settle, nil, t.chatAllowed(deps))
		if settled {
			_, err := t.appendFeedLine(chatID, settle, deps, t.chatAllowed(deps))
			if err != nil {
				t.rep.Error("Error in incoming feed loop")
				if !sleepContext(ctx, time.Second) {
					return
				}
				continue
			}
			continue
		}

		if soonest >= 0 {
			// A burst is pending but not quiet yet: sleep until it would
			// settle, then re-scan (a new message resets its timer).
			if !sleepContext(ctx, secondsToDuration(soonest)) {
				return
			}
			continue
		}
		// Nothing pending: block on new activity. Messages in other chats also
		// wake this loop, so re-scan rather than return.
		if !t.waitIdle(ctx) {
			return
		}
	}
}

// chatAllowed is the allowlist predicate the scans and snapshots share.
func (t *tracker) chatAllowed(deps Deps) func(int64) bool {
	if !deps.chatGate().Enabled() {
		return nil
	}
	return func(chatID int64) bool {
		return kit.ChatAllowed(deps.Allowlist, chatID, nil)
	}
}

// touchFeedFile creates the feed file, or fixes the mode of an existing one,
// before the consumer starts.
func touchFeedFile(deps Deps) error {
	f, err := openFeedAppend(deps)
	if err != nil {
		return err
	}
	return f.Close()
}

// openFeedAppend opens the feed for appending, enforcing 0600: it holds
// private contact metadata. A create-mode alone only applies while creating,
// so an existing or externally rotated 0644 file would keep its permissions;
// chmod on the open descriptor fixes that without a TOCTOU window.
func openFeedAppend(deps Deps) (*os.File, error) {
	path := deps.feedFilePath()
	if deps.FeedFile == "" {
		// Auto-create only the directory this module owns; an explicit path
		// must already exist so a typo fails loudly.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.Mode().Perm() != 0o600 {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// writeFeedLine appends one JSON line. The file is append-only: rotate it by
// hand (tail -F survives rotation).
func writeFeedLine(deps Deps, line feedLine) error {
	f, err := openFeedAppend(deps)
	if err != nil {
		return err
	}
	defer f.Close()

	encoded, err := encodeJSONPayload(line)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(encoded + "\n"); err != nil {
		return err
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func secondsToDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// wallSeconds is the feed timestamp as fractional epoch seconds.
func wallSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}

// round2 mirrors Python's round(x, 2) for the burst duration and feed
// timestamp fields.
func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func formatInt(value int64) string { return strconv.FormatInt(value, 10) }
