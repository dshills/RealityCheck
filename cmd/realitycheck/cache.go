package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dshills/realitycheck/internal/cache"
)

// newCacheCmd manages the result cache that check reads and writes.
func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the result cache",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Remove all cached results",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openDefaultCache()
			if err != nil {
				return err
			}
			removed, err := store.Clear()
			if err != nil {
				return &exitError{exitCodeGeneral, fmt.Sprintf("error: %v", err)}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %d cached results from %s\n", removed, store.Dir())
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show cache location and statistics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openDefaultCache()
			if err != nil {
				return err
			}
			entries, size, err := store.Stats()
			if err != nil {
				return &exitError{exitCodeGeneral, fmt.Sprintf("error: %v", err)}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "directory: %s\nentries:   %d\nsize:      %d bytes\n", store.Dir(), entries, size)
			return err
		},
	})
	return cmd
}

func openDefaultCache() (*cache.Store, error) {
	dir, err := cache.DefaultDir()
	if err != nil {
		return nil, &exitError{exitCodeGeneral, fmt.Sprintf("error: %v", err)}
	}
	store, err := cache.Open(dir)
	if err != nil {
		return nil, &exitError{exitCodeGeneral, fmt.Sprintf("error: %v", err)}
	}
	return store, nil
}
