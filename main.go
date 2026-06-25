package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/owenps/watchtower/internal/config"
	"github.com/owenps/watchtower/internal/logx"
	"github.com/owenps/watchtower/internal/store"
	"github.com/owenps/watchtower/internal/tui"
)

func main() {
	debug := flag.Bool("debug", false, "write debug logs to config dir")
	flag.Parse()

	cfg, cfgPath, err := config.LoadOrCreate()
	if err != nil {
		fatal(err)
	}

	cfgDir, err := config.Dir()
	if err != nil {
		fatal(err)
	}
	logger, closeLog, err := logx.Open(filepath.Join(cfgDir, "debug.log"), *debug)
	if err != nil {
		fatal(err)
	}
	defer closeLog()

	rules, err := cfg.RepoRules()
	if err != nil {
		fatal(err)
	}
	statePath, err := config.StatePath()
	if err != nil {
		fatal(err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		fatal(err)
	}
	defer st.Close()

	p := tea.NewProgram(tui.New(cfg, cfgPath, rules, st, logger), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "watchtower:", err)
	os.Exit(1)
}
