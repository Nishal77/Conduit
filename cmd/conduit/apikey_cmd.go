package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// apiKeyJSON is the exact shape `conduit apikey list --json` prints
// (spec/08-cli.md §6) — deliberately a separate type from store.APIKey so
// key_hash (and the Go-cased field names pgx scans into) never leak into
// output a script or human might parse.
type apiKeyJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

func newAPIKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apikey",
		Short: "Manage API keys",
	}
	cmd.AddCommand(newAPIKeyCreateCmd(), newAPIKeyListCmd(), newAPIKeyRevokeCmd())
	return cmd
}

func newAPIKeyCreateCmd() *cobra.Command {
	var (
		configPath string
		name       string
		tenantSlug string
		expires    string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API key for a tenant",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || tenantSlug == "" {
				return fmt.Errorf("--name and --tenant are required")
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

			var expiresAt *time.Time
			if expires != "" {
				t, err := parseExpiry(expires)
				if err != nil {
					return fmt.Errorf("invalid --expires: %w", err)
				}
				expiresAt = t
			}

			rawKey, keyHash, keyPrefix, err := auth.GenerateAPIKey()
			if err != nil {
				return fmt.Errorf("generate api key: %w", err)
			}

			apiKeys := store.NewAPIKeyStore(db)
			created, err := apiKeys.Create(ctx, tenant.ID, name, keyHash, keyPrefix, []string{"mcp:call"}, expiresAt)
			if err != nil {
				return fmt.Errorf("create api key: %w", err)
			}

			expiresDisplay := "never"
			if created.ExpiresAt != nil {
				expiresDisplay = created.ExpiresAt.Format(time.RFC3339)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "API Key created successfully")
			fmt.Fprintln(out)
			fmt.Fprintf(out, "Name:       %s\n", created.Name)
			fmt.Fprintf(out, "Tenant:     %s\n", tenantSlug)
			fmt.Fprintf(out, "Key:        %s\n", rawKey)
			fmt.Fprintf(out, "Key prefix: %s\n", created.KeyPrefix)
			fmt.Fprintf(out, "Expires:    %s\n", expiresDisplay)
			fmt.Fprintf(out, "Created:    %s\n", created.CreatedAt.Format(time.RFC3339))
			fmt.Fprintln(out)
			fmt.Fprintln(out, "⚠  Store this key securely — it will not be shown again.")
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&name, "name", "", "name/description for this API key (required)")
	cmd.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (required)")
	cmd.Flags().StringVar(&expires, "expires", "", `expiry duration, e.g. "30d", "90d", "1y" (default: never)`)
	return cmd
}

func newAPIKeyListCmd() *cobra.Command {
	var (
		configPath string
		tenantSlug string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API keys for a tenant",
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

			apiKeys := store.NewAPIKeyStore(db)
			keys, err := apiKeys.List(ctx, tenant.ID)
			if err != nil {
				return fmt.Errorf("list api keys: %w", err)
			}

			if asJSON {
				out := make([]apiKeyJSON, len(keys))
				for i, k := range keys {
					out[i] = apiKeyJSON{
						ID:         k.ID.String(),
						Name:       k.Name,
						KeyPrefix:  k.KeyPrefix,
						Scopes:     k.Scopes,
						CreatedAt:  k.CreatedAt,
						LastUsedAt: k.LastUsedAt,
						ExpiresAt:  k.ExpiresAt,
					}
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tPREFIX\tCREATED\tLAST USED")
			for _, k := range keys {
				lastUsed := "never"
				if k.LastUsedAt != nil {
					lastUsed = k.LastUsedAt.Format("2006-01-02 15:04:05")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					k.ID, k.Name, k.KeyPrefix, k.CreatedAt.Format("2006-01-02 15:04:05"), lastUsed)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON array")
	return cmd
}

func newAPIKeyRevokeCmd() *cobra.Command {
	var (
		configPath string
		force      bool
	)
	cmd := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid api key id %q: %w", args[0], err)
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

			if !force && !confirm(cmd, fmt.Sprintf("Revoke API key %s? [y/N] ", id)) {
				fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}

			apiKeys := store.NewAPIKeyStore(db)
			if err := apiKeys.Revoke(ctx, id); err != nil {
				return fmt.Errorf("revoke api key: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "API key revoked.")
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	return cmd
}

// confirm prompts the user with a y/N question on stdin. Any input other
// than "y"/"yes" (case-insensitive) is treated as "no".
func confirm(cmd *cobra.Command, prompt string) bool {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// parseExpiry parses a duration like "30d", "90d", "1y", or anything
// time.ParseDuration accepts ("720h"), returning the resulting expiry
// timestamp. time.ParseDuration has no day/year unit, so those two are
// handled explicitly.
func parseExpiry(s string) (*time.Time, error) {
	var d time.Duration
	switch {
	case strings.HasSuffix(s, "d"):
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return nil, fmt.Errorf("invalid day count in %q: %w", s, err)
		}
		d = time.Duration(days) * 24 * time.Hour
	case strings.HasSuffix(s, "y"):
		years, err := strconv.Atoi(strings.TrimSuffix(s, "y"))
		if err != nil {
			return nil, fmt.Errorf("invalid year count in %q: %w", s, err)
		}
		d = time.Duration(years) * 365 * 24 * time.Hour
	default:
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("unrecognized duration %q: %w", s, err)
		}
		d = parsed
	}
	t := time.Now().Add(d)
	return &t, nil
}
