package policy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// Loader watches a policy file and reloads it into an Engine when changed,
// either via a filesystem event or a periodic fallback tick — the fallback
// exists because fsnotify doesn't reliably fire for every editor's save
// pattern (e.g. some replace-via-rename instead of write-in-place).
type Loader struct {
	filePath string
	engine   *Engine
	interval time.Duration
	log      zerolog.Logger

	wg     sync.WaitGroup
	stopCh chan struct{}
}

// NewLoader returns a Loader that will keep engine in sync with the rules
// in filePath, checking at least every interval.
func NewLoader(filePath string, engine *Engine, interval time.Duration, log zerolog.Logger) *Loader {
	return &Loader{
		filePath: filePath,
		engine:   engine,
		interval: interval,
		log:      log,
		stopCh:   make(chan struct{}),
	}
}

// Start loads the policy file immediately (so the engine has its rules
// before the proxy accepts traffic), then watches for changes in the
// background until Stop is called or ctx is cancelled.
func (l *Loader) Start(ctx context.Context) error {
	if err := l.load(); err != nil {
		return fmt.Errorf("initial policy load: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create policy file watcher: %w", err)
	}
	if err := watcher.Add(l.filePath); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("watch policy file: %w", err)
	}

	l.wg.Add(1)
	go l.watchLoop(ctx, watcher)
	return nil
}

// Stop signals the background watch loop to exit and waits for it to do so.
func (l *Loader) Stop() {
	close(l.stopCh)
	l.wg.Wait()
}

func (l *Loader) watchLoop(ctx context.Context, watcher *fsnotify.Watcher) {
	defer l.wg.Done()
	defer func() { _ = watcher.Close() }()

	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				l.reload()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			l.log.Warn().Err(err).Msg("policy file watcher error")
		case <-ticker.C:
			l.reload()
		}
	}
}

// reload calls load and logs (rather than propagates) any error — a
// malformed policy file mid-flight must not take down an already-running
// proxy; it just keeps evaluating the last good rule set.
func (l *Loader) reload() {
	if err := l.load(); err != nil {
		l.log.Warn().Err(err).Msg("failed to reload policy file, keeping previous rules")
	}
}

// load reads and parses the policy file, validates it, and installs the
// result into the engine.
func (l *Loader) load() error {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		return fmt.Errorf("read policy file: %w", err)
	}

	var pf PolicyFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parse policy yaml: %w", err)
	}

	if err := validateRules(pf.Rules); err != nil {
		return fmt.Errorf("validate policy rules: %w", err)
	}

	l.engine.LoadRules(pf.Rules)
	l.log.Info().Int("count", len(pf.Rules)).Msg("policy rules loaded")
	return nil
}

// validateRules checks rule names are unique and every action (and its
// required fields) is valid, so a typo in policies.yaml fails loudly at
// load time instead of silently misbehaving at evaluation time.
func validateRules(rules []Rule) error {
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		if r.Name == "" {
			return errors.New("rule missing name")
		}
		if seen[r.Name] {
			return fmt.Errorf("duplicate rule name: %q", r.Name)
		}
		seen[r.Name] = true

		switch r.Action {
		case ActionAllow, ActionDeny, ActionLog:
		case ActionRateLimit:
			if r.RateLimit == nil {
				return fmt.Errorf("rule %q has action rate_limit but no rate_limit config", r.Name)
			}
		default:
			return fmt.Errorf("rule %q has invalid action: %q", r.Name, r.Action)
		}
	}
	return nil
}
