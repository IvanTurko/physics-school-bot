package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

var ctx = context.Background()

// now is the store's clock in the tests.
var now = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *sqlite.Store {
	return sqlite.New(testkit.NewDB(t), clock.NewFake(now))
}

// newUser registers a user with this Telegram ID and returns its ID.
func newUser(t *testing.T, s *sqlite.Store, tgID int64) int64 {
	u, err := s.EnsureUser(ctx, tgID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestOpenSkipsAppliedMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	for range 2 {
		db, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureUserRefreshesNames(t *testing.T) {
	s := newStore(t)
	first, err := s.EnsureUser(ctx, 42, "old", "Иван")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnsureUser(ctx, 42, "new", "Иван Т.")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.TelegramID != 42 || again.Username != "new" || again.Name != "Иван Т." {
		t.Fatalf("got %+v after %+v", again, first)
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	want := []domain.Message{
		{Role: domain.RoleUser, Content: "есть время во вторник?"},
		{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "c1", Name: "get_free_slots", Arguments: "{}"}}},
		{Role: domain.RoleTool, ToolCallID: "c1", Content: `{"ok":true}`},
		{Role: domain.RoleAssistant, Content: "Есть 19:00."},
	}
	if err := s.Append(ctx, user, want...); err != nil {
		t.Fatal(err)
	}
	got, err := s.Last(ctx, user, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestLastStartsWithUserMessage(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	err := s.Append(ctx, user,
		domain.Message{Role: domain.RoleUser, Content: "раз"},
		domain.Message{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "c1", Name: "get_free_slots"}}},
		domain.Message{Role: domain.RoleTool, ToolCallID: "c1", Content: "{}"},
		domain.Message{Role: domain.RoleAssistant, Content: "ответ"},
		domain.Message{Role: domain.RoleUser, Content: "два"},
		domain.Message{Role: domain.RoleAssistant, Content: "ещё ответ"},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Last(ctx, user, 4) // the last 4 start at the tool result
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "два" {
		t.Fatalf("got %+v, want the window to start at «два»", got)
	}
}

func TestHistoryIsPerUser(t *testing.T) {
	s := newStore(t)
	a, b := newUser(t, s, 1), newUser(t, s, 2)
	if err := s.Append(ctx, a, domain.Message{Role: domain.RoleUser, Content: "a"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Last(ctx, b, 40)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want nothing", got, err)
	}
}

// TestLastReadsSavedToolCalls writes the row by hand: saving and reading back
// would pass whatever format the column took.
func TestLastReadsSavedToolCalls(t *testing.T) {
	db := testkit.NewDB(t)
	s := sqlite.New(db, clock.Real{})
	user := newUser(t, s, 42)
	_, err := db.Exec(`INSERT INTO messages (user_id, role, tool_calls, created_at) VALUES
		(?, 'user', NULL, 0),
		(?, 'assistant', '[{"id":"c1","name":"get_free_slots","arguments":"{}"}]', 0)`, user, user)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Last(ctx, user, 40)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ToolCall{{ID: "c1", Name: "get_free_slots", Arguments: "{}"}}
	if len(got) != 2 || !reflect.DeepEqual(got[1].ToolCalls, want) {
		t.Fatalf("got %+v, want the saved call decoded", got)
	}
}

func TestDialogSkipsTools(t *testing.T) {
	s := newStore(t)
	u, err := s.EnsureUser(ctx, 42, "ivan", "Иван")
	if err != nil {
		t.Fatal(err)
	}
	// a tool call's text is left out with the call; another student wrote last
	err = s.Append(ctx, u.ID,
		domain.Message{Role: domain.RoleUser, Content: "есть время?"},
		domain.Message{Role: domain.RoleAssistant, Content: "Смотрю", ToolCalls: []domain.ToolCall{{ID: "c1", Name: "get_free_slots"}}},
		domain.Message{Role: domain.RoleTool, ToolCallID: "c1", Content: `{"ok":true}`},
		domain.Message{Role: domain.RoleAssistant, Content: "Есть 19:00."},
		domain.Message{Role: domain.RoleUser, Content: "беру"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, newUser(t, s, 43), domain.Message{Role: domain.RoleUser, Content: "чужое"}); err != nil {
		t.Fatal(err)
	}

	line := func(role domain.Role, text string) domain.Line {
		return domain.Line{Role: role, Text: text, At: time.Unix(now.Unix(), 0)}
	}
	talk := []domain.Line{line(domain.RoleUser, "есть время?"), line(domain.RoleAssistant, "Есть 19:00."), line(domain.RoleUser, "беру")}

	if got, n, err := s.CountDialog(ctx, u.ID); err != nil || got.Name != "Иван" || got.Username != "ivan" || n != 3 {
		t.Errorf("user, count = %+v, %d, %v; want Иван ivan, 3", got, n, err)
	}
	for _, tc := range []struct {
		offset, limit int
		want          []domain.Line
	}{
		{0, 2, talk[1:]},
		{2, 2, talk[:1]},
	} {
		got, err := s.ListDialog(ctx, u.ID, tc.offset, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("lines from %d = %v, want %v", tc.offset, got, tc.want)
		}
	}
}

func TestAddSlotsSkipsExisting(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	if n, err := s.AddSlots(ctx, []time.Time{at}); err != nil || n != 1 {
		t.Fatalf("first: %d, %v; want 1", n, err)
	}
	if n, err := s.AddSlots(ctx, []time.Time{at, at.Add(time.Hour)}); err != nil || n != 1 {
		t.Fatalf("again: %d, %v; want only the new slot added", n, err)
	}
}

// addSlot adds a free slot at start and returns its ID.
func addSlot(ctx context.Context, t *testing.T, s *sqlite.Store, start time.Time) int64 {
	if _, err := s.AddSlots(ctx, []time.Time{start}); err != nil {
		t.Fatal(err)
	}
	slot, err := s.SlotAt(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	return slot.ID
}

// book adds a slot at start and books it for the user.
func book(ctx context.Context, t *testing.T, s *sqlite.Store, user int64, start time.Time, grade int) domain.BookingView {
	v, err := s.InsertBooking(ctx, domain.Booking{UserID: user, SlotID: addSlot(ctx, t, s, start), Grade: grade, Status: domain.StatusNew})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAddSlotsSkipsDeleted(t *testing.T) {
	s := newStore(t)
	at := now.Add(24 * time.Hour)
	if _, err := s.DeleteFreeSlot(ctx, addSlot(ctx, t, s, at)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.AddSlots(ctx, []time.Time{at}); err != nil || n != 0 {
		t.Fatalf("added = %d, %v; want 0", n, err)
	}
}

func TestOpenSlotsRestoresDeleted(t *testing.T) {
	s := newStore(t)
	at := now.Add(24 * time.Hour)
	if _, err := s.DeleteFreeSlot(ctx, addSlot(ctx, t, s, at)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.OpenSlots(ctx, []time.Time{at, at.Add(time.Hour)}); err != nil || n != 2 {
		t.Fatalf("opened = %d, %v; want 2", n, err)
	}
	if n, err := s.OpenSlots(ctx, []time.Time{at}); err != nil || n != 0 {
		t.Fatalf("again: opened = %d, %v; want 0", n, err)
	}
}

func TestDeleteFreeSlot(t *testing.T) {
	s := newStore(t)
	at := now.Add(24 * time.Hour)
	free := addSlot(ctx, t, s, at)
	held := book(ctx, t, s, newUser(t, s, 42), at.Add(time.Hour), 9)

	if ok, err := s.DeleteFreeSlot(ctx, free); err != nil || !ok {
		t.Fatalf("deleted = %v, %v; want true", ok, err)
	}
	if ok, err := s.DeleteFreeSlot(ctx, free); err != nil || ok {
		t.Errorf("again: deleted = %v, %v; want false", ok, err)
	}
	if ok, err := s.DeleteFreeSlot(ctx, held.SlotID); err != nil || ok {
		t.Errorf("held slot: deleted = %v, %v; want false", ok, err)
	}
	if _, err := s.SlotAt(ctx, at); !errors.Is(err, domain.ErrSlotNotFound) {
		t.Errorf("SlotAt err = %v, want ErrSlotNotFound", err)
	}
	if slots, err := s.FreeSlots(ctx, at, at.Add(2*time.Hour), 10); err != nil || len(slots) != 0 {
		t.Errorf("free slots = %v, %v; want none", slots, err)
	}
}

func TestSlotsAhead(t *testing.T) {
	s := newStore(t)
	user, err := s.EnsureUser(ctx, 42, "", "Иван")
	if err != nil {
		t.Fatal(err)
	}
	// a slot starting now has started, and a cancelled booking holds nothing
	addSlot(ctx, t, s, now)
	held := book(ctx, t, s, user.ID, now.Add(time.Hour), 9)
	cancelled := book(ctx, t, s, user.ID, now.Add(2*time.Hour), 9)
	cancelled.Status = domain.StatusCancelled
	if _, err := s.UpdateBooking(ctx, cancelled.Booking); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteFreeSlot(ctx, addSlot(ctx, t, s, now.Add(3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	last := addSlot(ctx, t, s, now.Add(4*time.Hour))

	slot := func(id int64, after time.Duration) domain.SlotView {
		return domain.SlotView{Slot: domain.Slot{ID: id, Start: time.Unix(now.Add(after).Unix(), 0)}}
	}
	taken := slot(held.SlotID, time.Hour)
	taken.BookingID, taken.Name = held.ID, "Иван"
	ahead := []domain.SlotView{taken, slot(cancelled.SlotID, 2*time.Hour), slot(last, 4*time.Hour)}

	if n, err := s.CountSlots(ctx); err != nil || n != 3 {
		t.Errorf("count = %d, %v; want 3", n, err)
	}
	for _, tc := range []struct {
		offset, limit int
		want          []domain.SlotView
	}{
		{0, 10, ahead},
		{1, 1, ahead[1:2]},
	} {
		got, err := s.ListSlots(ctx, tc.offset, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("slots from %d = %v, want %v", tc.offset, got, tc.want)
		}
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	refused := errors.New("refused")

	err := s.InTx(ctx, func(ctx context.Context) error {
		book(ctx, t, s, user, time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), 9)
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the error of fn", err)
	}
	if p, err := s.Profile(ctx, user); err != nil || p.Last != nil {
		t.Fatalf("profile = %+v, %v; want no booking saved", p, err)
	}
}

func TestProfileTakesLatestBooking(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	if p, err := s.Profile(ctx, user); err != nil || p.Last != nil {
		t.Fatalf("new user: %+v, %v; want nothing known", p, err)
	}

	at := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	first := book(ctx, t, s, user, at, 8)
	first.Status = domain.StatusCancelled
	if _, err := s.UpdateBooking(ctx, first.Booking); err != nil {
		t.Fatal(err)
	}
	second := book(ctx, t, s, user, at.Add(time.Hour), 9)

	p, err := s.Profile(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if p.Last == nil || p.Last.Grade != 9 {
		t.Errorf("last = %+v, want the newer booking", p.Last)
	}
	if p.Active == nil || p.Active.ID != second.ID {
		t.Errorf("active = %+v, want booking %d", p.Active, second.ID)
	}
}

func TestProfileSeesOnlyOwnHeldLesson(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	other := newUser(t, s, 43)
	at := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	mark := func(v domain.BookingView, to domain.Status) {
		v.Status = to
		if _, err := s.UpdateBooking(ctx, v.Booking); err != nil {
			t.Fatal(err)
		}
	}
	check := func(step string, want bool) {
		p, err := s.Profile(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if p.TrialHeld != want {
			t.Errorf("%s: trial held = %v, want %v", step, p.TrialHeld, want)
		}
	}

	first := book(ctx, t, s, user, at, 9)
	check("booked", false)
	mark(first, domain.StatusNoShow)
	check("no-show", false)
	mark(book(ctx, t, s, other, at.Add(time.Hour), 9), domain.StatusDone)
	check("another user's lesson held", false)
	mark(book(ctx, t, s, user, at.Add(2*time.Hour), 9), domain.StatusDone)
	check("lesson held", true)
}

func TestConfirmedBookingHoldsSlot(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	start := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	v := book(ctx, t, s, user, start, 9)
	v.Status = domain.StatusConfirmed
	if _, err := s.UpdateBooking(ctx, v.Booking); err != nil {
		t.Fatal(err)
	}

	if free, err := s.FreeSlots(ctx, start, start.Add(time.Hour), 10); err != nil || len(free) != 0 {
		t.Errorf("free slots = %v, %v; want the confirmed one held", free, err)
	}
	if active, err := s.ActiveBooking(ctx, user); err != nil || active.ID != v.ID {
		t.Errorf("active = %+v, %v; want the confirmed booking", active, err)
	}
	other := v.Booking
	other.UserID, other.Status = newUser(t, s, 43), domain.StatusNew
	if _, err := s.InsertBooking(ctx, other); !errors.Is(err, domain.ErrSlotTaken) {
		t.Errorf("err = %v, want ErrSlotTaken", err)
	}
}

func TestBookingCarriesTelegramID(t *testing.T) {
	s := newStore(t)
	v := book(ctx, t, s, newUser(t, s, 42), time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), 9)
	if v.User.TelegramID != 42 {
		t.Fatalf("TelegramID = %d, want 42", v.User.TelegramID)
	}
}

func TestCardsArePerBooking(t *testing.T) {
	s := newStore(t)
	b1 := book(ctx, t, s, newUser(t, s, 42), time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), 9)
	b2 := book(ctx, t, s, newUser(t, s, 43), time.Date(2026, 10, 7, 17, 0, 0, 0, time.UTC), 9)
	first := domain.Card{BookingID: b1.ID, ChatID: 101, MessageID: 10}
	for _, c := range []domain.Card{first, {BookingID: b2.ID, ChatID: 101, MessageID: 11}} {
		if err := s.AddCard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Cards(ctx, b1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []domain.Card{first}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cards = %v, want %v", got, want)
	}
}

func TestCountBookings(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	for _, b := range []struct {
		after  time.Duration
		status domain.Status
	}{
		{time.Hour, domain.StatusNew},
		{0, domain.StatusNew}, // a lesson starting now awaits its mark
		{-time.Hour, domain.StatusConfirmed},
		{-2 * time.Hour, domain.StatusDone},
	} {
		v := book(ctx, t, s, user, now.Add(b.after), 9)
		v.Status = b.status
		if _, err := s.UpdateBooking(ctx, v.Booking); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.CountBookings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[domain.Filter]int{domain.FilterNew: 1, domain.FilterAwaiting: 2, domain.FilterDone: 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
}

func TestListBookings(t *testing.T) {
	s := newStore(t)
	user := newUser(t, s, 42)
	afters := []time.Duration{-time.Hour, 30 * time.Minute, 2 * time.Hour, -3 * time.Hour} // any other order gives another page
	cancelled := make([]int64, len(afters))
	for i, after := range afters {
		v := book(ctx, t, s, user, now.Add(after), 9)
		v.Status = domain.StatusCancelled
		if _, err := s.UpdateBooking(ctx, v.Booking); err != nil {
			t.Fatal(err)
		}
		cancelled[i] = v.ID
	}
	awaiting := book(ctx, t, s, user, now.Add(-4*time.Hour), 9)

	for _, tc := range []struct {
		f             domain.Filter
		offset, limit int
		want          []int64
	}{
		{domain.FilterAwaiting, 0, 10, []int64{awaiting.ID}},
		{domain.FilterCancelled, 1, 2, []int64{cancelled[0], cancelled[2]}},
	} {
		views, err := s.ListBookings(ctx, tc.f, tc.offset, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, v := range views {
			got = append(got, v.ID)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s bookings = %v, want %v", tc.f, got, tc.want)
		}
	}
}

func TestUpcomingListsActiveAhead(t *testing.T) {
	s := newStore(t)
	later := book(ctx, t, s, newUser(t, s, 1), now.Add(2*time.Hour), 9)
	sooner := book(ctx, t, s, newUser(t, s, 2), now.Add(time.Hour), 9)
	cancelled := book(ctx, t, s, newUser(t, s, 3), now.Add(3*time.Hour), 9)
	book(ctx, t, s, newUser(t, s, 4), now, 9)

	sooner.Status, cancelled.Status = domain.StatusConfirmed, domain.StatusCancelled
	for _, b := range []domain.Booking{sooner.Booking, cancelled.Booking} {
		if _, err := s.UpdateBooking(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Upcoming(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(now.Unix(), 0)
	want := []domain.Lesson{{BookingView: sooner, Booked: at, Reminded: at}, {BookingView: later, Booked: at, Reminded: at}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upcoming = %v, want the confirmed and the new one ahead, earliest first", got)
	}
}

func TestNewSlotRestartsReminders(t *testing.T) {
	c := clock.NewFake(now)
	s := sqlite.New(testkit.NewDB(t), c)
	ours := book(ctx, t, s, newUser(t, s, 1), now.Add(24*time.Hour), 9)
	book(ctx, t, s, newUser(t, s, 2), now.Add(48*time.Hour), 9)
	t0 := time.Unix(now.Unix(), 0)

	// check compares the reminder times of ours, which starts first, and the other lesson.
	check := func(step string, ourReminder time.Time) {
		t.Helper()
		got, err := s.Upcoming(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reminded := make([]time.Time, len(got))
		for i, l := range got {
			reminded[i] = l.Reminded
		}
		if want := []time.Time{ourReminder, t0}; !slices.EqualFunc(reminded, want, time.Time.Equal) {
			t.Fatalf("%s: reminded %v, want %v", step, reminded, want)
		}
	}

	c.Advance(time.Minute)
	if err := s.MarkReminded(ctx, ours.ID); err != nil {
		t.Fatal(err)
	}
	check("after the reminder", t0.Add(time.Minute))

	c.Advance(time.Minute)
	ours.Status = domain.StatusConfirmed
	if _, err := s.UpdateBooking(ctx, ours.Booking); err != nil {
		t.Fatal(err)
	}
	check("after the confirmation", t0.Add(time.Minute))

	c.Advance(time.Minute)
	ours.SlotID = addSlot(ctx, t, s, now.Add(25*time.Hour))
	if _, err := s.UpdateBooking(ctx, ours.Booking); err != nil {
		t.Fatal(err)
	}
	check("after the new slot", t0.Add(3*time.Minute))
}

func TestUnsyncedFollowsVersions(t *testing.T) {
	c := clock.NewFake(now)
	s := sqlite.New(testkit.NewDB(t), c)
	a := book(ctx, t, s, newUser(t, s, 1), now.Add(24*time.Hour), 9)
	b := book(ctx, t, s, newUser(t, s, 2), now.Add(48*time.Hour), 9)

	// check compares the unsynced bookings as id@version and when each was made.
	check := func(step string, want ...string) {
		t.Helper()
		leads, err := s.Unsynced(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(leads))
		for i, l := range leads {
			got[i] = fmt.Sprintf("%d@%d %s", l.ID, l.Version, l.Booked.UTC().Format("15:04"))
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: unsynced %v, want %v", step, got, want)
		}
	}
	mark := func(id int64, version int) {
		t.Helper()
		if err := s.MarkSynced(ctx, id, version); err != nil {
			t.Fatal(err)
		}
	}
	save := func(v domain.BookingView, status domain.Status) {
		t.Helper()
		v.Status = status
		if _, err := s.UpdateBooking(ctx, v.Booking); err != nil {
			t.Fatal(err)
		}
	}

	check("new", "1@1 09:00", "2@1 09:00")
	mark(a.ID, 1)
	check("a marked", "2@1 09:00")
	mark(b.ID, 1)
	check("b marked")

	c.Advance(time.Minute)
	if err := s.MarkReminded(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	check("reminded")
	save(a, domain.StatusConfirmed)
	check("confirmed", "1@2 09:00")
	mark(a.ID, 1)
	check("marked at the old version", "1@2 09:00")
	save(a, domain.StatusCancelled)
	check("cancelled", "1@3 09:00")
}
