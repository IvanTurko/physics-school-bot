package e2e

import (
	"strings"
	"testing"

	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

func TestDemoAnswersWithTheTerm(t *testing.T) {
	h := testkit.Start(t, testkit.Options{Now: now, AdminIDs: []int64{admin}, Demo: true})
	h.TG.UserSays(user, "/demo")
	if reply := h.TG.WaitMessage(user); !strings.Contains(reply, "07.10 12:00") {
		t.Errorf("reply = %q, want the rights until 07.10 12:00", reply)
	}
}

func TestDemoNeedsDemoMode(t *testing.T) {
	h := start(t)
	h.LLM.Reply(testkit.Text("Не понимаю команду"))
	h.TG.UserSays(user, "/demo")
	msgs := h.LLM.WaitRequest().Messages
	if got := msgs[len(msgs)-1].Content; got != "/demo" {
		t.Errorf("model got %q, want /demo as a plain message", got)
	}
}

// phoneNotice is how the greeting says what the phone is for.
const phoneNotice = "только для связи по записи"

func TestStartPointsToDemo(t *testing.T) {
	h := testkit.Start(t, testkit.Options{Now: now, Demo: true})
	h.TG.UserSays(user, "/start")
	if reply := h.TG.WaitMessage(user); !strings.Contains(reply, phoneNotice) || !strings.Contains(reply, "/demo") {
		t.Errorf("reply = %q, want the phone notice and the /demo hint", reply)
	}
}

func TestStartHidesDemoOutsideDemoMode(t *testing.T) {
	h := testkit.Start(t, testkit.Options{Now: now})
	h.TG.UserSays(user, "/start")
	if reply := h.TG.WaitMessage(user); !strings.Contains(reply, phoneNotice) || strings.Contains(reply, "/demo") {
		t.Errorf("reply = %q, want the phone notice without the /demo hint", reply)
	}
}
