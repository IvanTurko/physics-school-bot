// Package worker runs the bot's background jobs.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
)

// SlotAdder creates slots, skipping every time that has a slot, even a deleted one.
type SlotAdder interface {
	AddSlots(ctx context.Context, starts []time.Time) (int, error)
}

// The demo schedule: every hour from the first to the last, each day of the week ahead.
const (
	demoDays      = 7
	demoFirstHour = 16
	demoLastHour  = 19
)

// DemoSlots keeps the demo schedule filled, so a reviewer can always book.
type DemoSlots struct {
	slots SlotAdder
	clock clock.Clock
	tz    *time.Location
	log   *slog.Logger
}

// NewDemoSlots creates DemoSlots that lay the hours out in tz.
func NewDemoSlots(slots SlotAdder, c clock.Clock, tz *time.Location, log *slog.Logger) *DemoSlots {
	return &DemoSlots{slots: slots, clock: c, tz: tz, log: log}
}

// Tick adds the demo slots of the week ahead that are still in the future.
func (d *DemoSlots) Tick(ctx context.Context) error {
	now := d.clock.Now().In(d.tz)
	var starts []time.Time
	for day := range demoDays {
		for hour := demoFirstHour; hour <= demoLastHour; hour++ {
			if t := time.Date(now.Year(), now.Month(), now.Day()+day, hour, 0, 0, 0, d.tz); t.After(now) {
				starts = append(starts, t)
			}
		}
	}
	added, err := d.slots.AddSlots(ctx, starts)
	if err != nil {
		return fmt.Errorf("cannot add demo slots: %w", err)
	}
	d.log.Info("demo slots added", "count", added)
	return nil
}

// Run ticks at once and then daily until ctx ends.
func (d *DemoSlots) Run(ctx context.Context) {
	daily := time.NewTicker(24 * time.Hour)
	defer daily.Stop()
	for {
		if err := d.Tick(ctx); err != nil {
			d.log.Error("demo slots", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-daily.C:
		}
	}
}
