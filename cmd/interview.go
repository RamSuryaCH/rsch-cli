package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/opencode-ai/opencode/internal/interview"
	"github.com/spf13/cobra"
	"os"
	"strings"
)

var interviewCmd = &cobra.Command{Use: "interview [goal]", Short: "Ask only repository-relevant clarification questions", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	goal, _ := cmd.Flags().GetString("goal")
	if len(args) == 1 {
		goal = args[0]
	}
	if strings.TrimSpace(goal) == "" {
		goal = "general engineering changes"
	}
	session, err := interview.Start(cmd.Context(), root, goal)
	if err != nil {
		return err
	}
	answers, _ := cmd.Flags().GetStringArray("answer")
	for _, raw := range answers {
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("answer must use id=value")
		}
		if err := session.Answer(parts[0], parts[1]); err != nil {
			return err
		}
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	info, _ := os.Stdin.Stat()
	if !jsonOutput && len(answers) == 0 && info != nil && info.Mode()&os.ModeCharDevice != 0 {
		reader := bufio.NewReader(os.Stdin)
		for i := range session.Questions {
			fmt.Printf("%s\n> ", session.Questions[i].Prompt)
			answer, readErr := reader.ReadString('\n')
			if readErr != nil && len(answer) == 0 {
				break
			}
			session.Questions[i].Answer = strings.TrimSpace(answer)
		}
	}
	if jsonOutput {
		data, err := json.MarshalIndent(session, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("Goal: %s\n", session.Goal)
	for _, evidence := range session.Evidence {
		fmt.Printf("Evidence: %s:%d %s\n", evidence.File, evidence.Line, evidence.Text)
	}
	if len(session.Questions) == 0 {
		fmt.Println("No unresolved clarification questions found.")
		return nil
	}
	for _, question := range session.Questions {
		fmt.Printf("%s [%s]\n", question.ID, question.Prompt)
	}
	requirements := session.Requirements()
	if len(requirements) > 0 {
		fmt.Printf("Confirmed requirements: %d\n", len(requirements))
	}
	return nil
}}

func init() {
	interviewCmd.Flags().String("goal", "", "Goal to clarify when no positional goal is supplied")
	interviewCmd.Flags().StringArray("answer", nil, "Answer in id=value form (repeatable)")
	interviewCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(interviewCmd)
}
