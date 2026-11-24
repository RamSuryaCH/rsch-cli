package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/dbintel"
	"github.com/spf13/cobra"
)

var databaseCmd = &cobra.Command{
	Use:   "database",
	Short: "Inspect the local database schema without executing writes",
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
		if err != nil {
			return err
		}
		defer connection.Close()
		schema, err := dbintel.InspectWorkspace(cmd.Context(), connection, root)
		if err != nil {
			return err
		}
		if err := recordEvidence(root, "database", "rsch database", "observed", fmt.Sprintf("inspected %d database tables and %d migration references", len(schema.Tables), len(schema.Migrations)), "", schema); err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			data, _ := json.MarshalIndent(schema, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		for _, table := range schema.Tables {
			fmt.Printf("%s: %d columns, %d foreign keys, %d indexes\n", table.Name, len(table.Columns), len(table.ForeignKeys), len(table.Indexes))
		}
		if len(schema.Tables) == 0 {
			fmt.Println("no tables")
		}
		return nil
	},
}

func init() {
	databaseCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(databaseCmd)
}
