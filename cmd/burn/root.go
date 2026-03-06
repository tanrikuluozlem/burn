package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "burn",
	Short: "Kubernetes cost intelligence",
	Long:  "Burn - Know where your money goes. AI-powered Kubernetes cost analysis and optimization.",
}
