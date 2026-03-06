package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "0.1.0"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("burn %s\n", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
