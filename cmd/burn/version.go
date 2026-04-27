package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("burn %s (commit: %s, built: %s)\n", version, commit, date)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
