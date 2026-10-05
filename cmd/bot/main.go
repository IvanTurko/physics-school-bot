// Command bot runs the Telegram bot of the physics school.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // embed the zone database: the server may lack it

	"github.com/IvanTurko/physics-school-bot/internal/app"
	"github.com/IvanTurko/physics-school-bot/internal/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("bot stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, app.Deps{Log: log})
	if err != nil {
		return err
	}
	defer func() {
		if err := a.Close(); err != nil {
			log.Error("cannot close database", "err", err)
		}
	}()
	return a.Run(ctx)
}
