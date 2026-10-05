package telegram

import (
	"context"
	"log/slog"
	"time"

	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// Updates is the source of incoming updates.
type Updates interface {
	GetUpdates(ctx context.Context, offset int64) ([]tg.Update, error)
}

// pollPause keeps a getUpdates that fails past its retries, such as one with a
// refused token, from spinning the loop.
const pollPause = 3 * time.Second

// Poll long-polls src until ctx ends and hands every update to push. An update
// is confirmed once pushed, not once handled.
func Poll(ctx context.Context, src Updates, push func(tg.Update), log *slog.Logger) {
	var offset int64
	for ctx.Err() == nil {
		ups, err := src.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error("cannot get updates", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(pollPause):
			}
			continue
		}
		for _, u := range ups {
			push(u)
			offset = u.UpdateID + 1
		}
	}
}
