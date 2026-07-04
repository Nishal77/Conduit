package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/conduit-oss/conduit/internal/audit"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/spf13/cobra"
)

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Query and tail the audit log",
	}
	cmd.AddCommand(newAuditTailCmd(), newAuditQueryCmd(), newAuditExportCmd())
	return cmd
}

func newAuditTailCmd() *cobra.Command {
	var (
		configPath string
		tenantSlug string
		toolFilter string
		since      string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream audit events for a tenant as they happen",
		Long:  "Phase 3 connects directly to PostgreSQL and polls (spec/07-audit.md §6); Phase 4's management API adds a proper SSE endpoint this command will switch to.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" {
				return fmt.Errorf("--tenant is required")
			}

			cfg, err := loadConfigPartial(cmd, configPath)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			db, err := connectDB(ctx, cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			tenants := store.NewTenantStore(db)
			tenant, err := tenants.GetBySlug(ctx, tenantSlug)
			if err != nil {
				return fmt.Errorf("tenant %q not found: %w", tenantSlug, err)
			}

			if since != "" {
				// --since is accepted for interface completeness with
				// spec/08-cli.md §8 but Stream always starts from "now" —
				// backfilling history is what `audit query --from` is for.
				fmt.Fprintf(cmd.ErrOrStderr(), "note: --since is not yet honored by tail; use 'conduit audit query --from %s' for history\n", since)
			}

			auditStore := store.NewAuditStore(db)
			events, err := auditStore.Stream(ctx, tenant.ID, store.AuditStreamFilter{ToolName: toolFilter})
			if err != nil {
				return fmt.Errorf("start audit stream: %w", err)
			}

			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			if !asJSON {
				fmt.Fprintln(w, "TIME\tTENANT\tTOOL\tSTATUS\tLATENCY\tPOLICY")
				w.Flush()
			}

			for e := range events {
				if asJSON {
					_ = json.NewEncoder(out).Encode(e)
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%dms\t%s\n",
					e.CreatedAt.Format("15:04:05.000"), tenantSlug, e.ToolName, e.StatusCode, e.LatencyMs, e.PolicyAction)
				w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug to stream events for (required)")
	cmd.Flags().StringVar(&toolFilter, "tool", "", `filter by tool name, e.g. "github/*"`)
	cmd.Flags().StringVar(&since, "since", "", `start from this time (e.g. "5m", "1h", RFC3339)`)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON events (default: pretty table)")
	return cmd
}

func newAuditQueryCmd() *cobra.Command {
	var (
		configPath string
		tenantSlug string
		from, to   string
		toolFilter string
		limit      int
		offset     int
		output     string
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query historical audit events",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" {
				return fmt.Errorf("--tenant is required")
			}

			cfg, err := loadConfigPartial(cmd, configPath)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			db, err := connectDB(ctx, cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			tenants := store.NewTenantStore(db)
			tenant, err := tenants.GetBySlug(ctx, tenantSlug)
			if err != nil {
				return fmt.Errorf("tenant %q not found: %w", tenantSlug, err)
			}

			q := store.AuditQuery{TenantID: tenant.ID, Limit: limit, Offset: offset, OrderDesc: true}
			if from != "" {
				t, err := parseTimeArg(from)
				if err != nil {
					return fmt.Errorf("invalid --from: %w", err)
				}
				q.FromTime = &t
			}
			if to != "" {
				t, err := parseTimeArg(to)
				if err != nil {
					return fmt.Errorf("invalid --to: %w", err)
				}
				q.ToTime = &t
			}
			if toolFilter != "" {
				q.ToolName = &toolFilter
			}

			auditStore := store.NewAuditStore(db)

			switch output {
			case "json":
				result, err := audit.Query(ctx, auditStore, q)
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)

			case "csv":
				return audit.ExportCSV(ctx, auditStore, q, cmd.OutOrStdout())

			default:
				result, err := audit.Query(ctx, auditStore, q)
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "TIME\tTOOL\tSTATUS\tLATENCY\tPOLICY\tCOST")
				for _, e := range result.Events {
					fmt.Fprintf(w, "%s\t%s\t%d\t%dms\t%s\t$%.4f\n",
						e.CreatedAt.Format("2006-01-02 15:04:05"), e.ToolName, e.StatusCode, e.LatencyMs, e.PolicyAction, e.CostUSD)
				}
				if err := w.Flush(); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\n%d of %d events\n", len(result.Events), result.Total)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (required)")
	cmd.Flags().StringVar(&from, "from", "", `start time (ISO 8601 or relative: "24h", "7d")`)
	cmd.Flags().StringVar(&to, "to", "", "end time (default: now)")
	cmd.Flags().StringVar(&toolFilter, "tool", "", "filter by tool name (exact or prefix with *)")
	cmd.Flags().IntVar(&limit, "limit", 50, "number of results (default: 50, max: 500)")
	cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
	cmd.Flags().StringVar(&output, "output", "table", `output format: "table" | "json" | "csv"`)
	return cmd
}

func newAuditExportCmd() *cobra.Command {
	var (
		configPath string
		tenantSlug string
		from, to   string
		format     string
		outputPath string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the full audit log for a time range",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || from == "" || to == "" {
				return fmt.Errorf("--tenant, --from, and --to are required")
			}
			if format != "csv" && format != "json" {
				return fmt.Errorf("--format must be csv or json")
			}

			cfg, err := loadConfigPartial(cmd, configPath)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			db, err := connectDB(ctx, cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			tenants := store.NewTenantStore(db)
			tenant, err := tenants.GetBySlug(ctx, tenantSlug)
			if err != nil {
				return fmt.Errorf("tenant %q not found: %w", tenantSlug, err)
			}

			fromTime, err := parseTimeArg(from)
			if err != nil {
				return fmt.Errorf("invalid --from: %w", err)
			}
			toTime, err := parseTimeArg(to)
			if err != nil {
				return fmt.Errorf("invalid --to: %w", err)
			}

			q := store.AuditQuery{TenantID: tenant.ID, FromTime: &fromTime, ToTime: &toTime}

			w := cmd.OutOrStdout()
			if outputPath != "" {
				f, err := os.Create(outputPath)
				if err != nil {
					return fmt.Errorf("create output file: %w", err)
				}
				defer f.Close()
				w = f
			}

			auditStore := store.NewAuditStore(db)

			var count int
			if format == "csv" {
				if err := audit.ExportCSV(ctx, auditStore, q, &countingWriter{w: w, n: &count}); err != nil {
					return err
				}
			} else {
				result, err := audit.Query(ctx, auditStore, q)
				if err != nil {
					return err
				}
				count = len(result.Events)
				if err := json.NewEncoder(w).Encode(result.Events); err != nil {
					return err
				}
			}

			dest := outputPath
			if dest == "" {
				dest = "stdout"
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Exported %d events to %s\n", count, dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (required)")
	cmd.Flags().StringVar(&from, "from", "", "start time (required)")
	cmd.Flags().StringVar(&to, "to", "", "end time (required)")
	cmd.Flags().StringVar(&format, "format", "csv", `export format: "csv" | "json"`)
	cmd.Flags().StringVar(&outputPath, "output", "", "output file path (default: stdout)")
	return cmd
}

// countingWriter wraps an io.Writer and counts newlines written after the
// header, giving ExportCSV's caller an event count without re-parsing the
// CSV it just streamed out.
type countingWriter struct {
	w          io.Writer
	n          *int
	seenHeader bool
}

func (c *countingWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if !c.seenHeader {
				c.seenHeader = true
				continue
			}
			*c.n++
		}
	}
	return c.w.Write(p)
}

// parseTimeArg parses either an RFC3339 timestamp or a relative duration
// like "24h" / "7d" (interpreted as "that long ago from now").
func parseTimeArg(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := parseExpiry("-" + strings.TrimSuffix(s, "d") + "d")
		if err == nil {
			return *days, nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (want RFC3339 or a duration like \"24h\"/\"7d\")", s)
}
