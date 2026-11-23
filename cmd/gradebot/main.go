// Command gradebot runs the bot: gradebot [--data data].
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	_ "time/tzdata" // time zones in an image without them

	"github.com/IvanIkra/gradebot/internal/app"
	"github.com/IvanIkra/gradebot/internal/config"
)

func main() {
	data := flag.String("data", "data", "directory for the database")
	level := flag.String("log-level", "INFO", "DEBUG, INFO, WARN or ERROR")
	readOnce := flag.Bool("read-once", false, "read every sheet once, send nothing, and tell how many new grades it "+
		"found and which tabs it laid out again: the check before this version takes over a database")
	flag.Parse()
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(*level))); err != nil {
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
	if err := config.LoadEnv(".env"); err != nil {
		fail(err)
	}
	env, err := config.FromEnviron(os.Getenv)
	if err != nil {
		fail(err)
	}
	service, err := app.New(env, *data)
	if err != nil {
		fail(err)
	}
	if *readOnce {
		found, relaid, err := service.ReadOnce()
		if err != nil {
			fail(err)
		}
		fmt.Printf("new grades found: %d; tabs laid out again: %d\n", found, len(relaid))
		for _, tab := range relaid {
			fmt.Println("  " + tab)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.Run(ctx); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gradebot:", err)
	os.Exit(1)
}
