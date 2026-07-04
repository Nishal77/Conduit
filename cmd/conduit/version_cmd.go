package main

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// versionInfo is what `conduit version --json` prints.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	Go      string `json:"go"`
}

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the Conduit version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := versionInfo{Version: version, Commit: commit, Built: built, Go: runtime.Version()}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "conduit %s (commit: %s, built: %s)\n", info.Version, info.Commit, info.Built)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}
