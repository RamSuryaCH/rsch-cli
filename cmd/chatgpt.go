package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/opencode-ai/opencode/internal/chatgpt"
	"github.com/spf13/cobra"
)

// newChatGPTClient is replaced in tests.
var newChatGPTClient = func() (*chatgpt.Client, error) {
	store, err := chatgpt.DefaultStore()
	if err != nil {
		return nil, err
	}
	return chatgpt.NewClient(store), nil
}

// openBrowser is replaced in tests.
var openBrowser = func(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to a model provider",
}

var loginChatGPTCmd = &cobra.Command{
	Use:   "chatgpt",
	Short: "Sign in with ChatGPT to use your ChatGPT plan (implemented, awaiting live verification)",
	Long: `Sign in with ChatGPT, following OpenAI's documented flow for open-source tools.

RSCH opens your browser at auth.openai.com with PKCE, state and nonce, and listens
for the callback on 127.0.0.1. On first sign-in OpenAI registers this installation
and issues a client ID; RSCH stores it with your tokens in chatgpt-auth.json in the
RSCH config directory (owner-only permissions). ChatGPT plan usage is enabled only
if you grant it (the chatgpt.tokens.use.direct scope).

Manage RSCH's access and usage in ChatGPT Settings: ` + chatgpt.ManageUsageURL,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newChatGPTClient()
		if err != nil {
			return err
		}
		newAccount, _ := cmd.Flags().GetBool("new-account")
		noBrowser, _ := cmd.Flags().GetBool("no-browser")
		port, _ := cmd.Flags().GetInt("port")
		stderr := cmd.ErrOrStderr()
		opener := openBrowser
		if noBrowser {
			opener = func(string) error { return errors.New("browser disabled") }
		}
		fmt.Fprintln(stderr, "Opening your browser to continue with ChatGPT...")
		record, err := client.Login(cmd.Context(), chatgpt.LoginOptions{
			OpenBrowser: opener,
			ShowURL: func(u string) {
				fmt.Fprintf(stderr, "Open this URL in your browser to continue:\n%s\n", u)
			},
			Port:       port,
			NewAccount: newAccount,
		})
		if err != nil {
			return err
		}
		writeChatGPTStatus(cmd.OutOrStdout(), record)
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Sign out of a model provider",
}

var logoutChatGPTCmd = &cobra.Command{
	Use:   "chatgpt",
	Short: "Sign out of ChatGPT: revoke the session and clear local tokens",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newChatGPTClient()
		if err != nil {
			return err
		}
		result, err := client.Logout(cmd.Context())
		if err != nil {
			return err
		}
		if result.Revoked {
			fmt.Fprintln(cmd.OutOrStdout(), "Signed out of ChatGPT. The session was revoked and local tokens were cleared.")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Signed out of ChatGPT locally. Local tokens were cleared.")
		}
		if result.Warning != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", result.Warning)
		}
		return nil
	},
}

var chatgptCmd = &cobra.Command{
	Use:   "chatgpt",
	Short: "Use your ChatGPT plan through Sign in with ChatGPT (implemented, awaiting live verification)",
}

var chatgptStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the ChatGPT connection and whether plan usage is enabled",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newChatGPTClient()
		if err != nil {
			return err
		}
		record, ok, err := client.Store.Load()
		if err != nil {
			return err
		}
		if !ok || !record.Connected() {
			fmt.Fprintln(cmd.OutOrStdout(), "Not signed in to ChatGPT. Run: rsch login chatgpt")
			return nil
		}
		writeChatGPTStatus(cmd.OutOrStdout(), record)
		return nil
	},
}

var chatgptModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List the models available to your ChatGPT plan",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newChatGPTClient()
		if err != nil {
			return err
		}
		models, err := client.ListModels(cmd.Context())
		if err != nil {
			return err
		}
		for _, m := range models {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", m.Slug, m.DisplayName)
		}
		return nil
	},
}

var chatgptAskCmd = &cobra.Command{
	Use:   "ask <prompt>",
	Short: "Send one prompt to the Responses API using your ChatGPT plan",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newChatGPTClient()
		if err != nil {
			return err
		}
		model, _ := cmd.Flags().GetString("model")
		if model == "" {
			if model, err = firstChatGPTModel(cmd.Context(), client); err != nil {
				return err
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Using ChatGPT plan (model %s). Manage usage: %s\n", model, chatgpt.ManageUsageURL)
		out := cmd.OutOrStdout()
		_, err = client.StreamResponse(cmd.Context(), chatgpt.ResponseRequest{Model: model, Input: strings.Join(args, " ")}, func(delta string) {
			fmt.Fprint(out, delta)
		})
		fmt.Fprintln(out)
		return err
	},
}

func firstChatGPTModel(ctx context.Context, client *chatgpt.Client) (string, error) {
	models, err := client.ListModels(ctx)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", errors.New("no models are available for this ChatGPT account")
	}
	return models[0].Slug, nil
}

func writeChatGPTStatus(w io.Writer, record chatgpt.Record) {
	account := record.Email
	if account == "" {
		account = record.Subject
	}
	fmt.Fprintf(w, "Connected to ChatGPT as %s.\n", account)
	if record.PlanUsage() {
		fmt.Fprintf(w, "Using ChatGPT plan. Manage usage: %s\n", chatgpt.ManageUsageURL)
	} else {
		fmt.Fprintf(w, "ChatGPT plan usage was not granted, so RSCH will not send requests on your plan.\nRun rsch login chatgpt again and allow it, or manage access at %s\n", chatgpt.ManageUsageURL)
	}
}

func init() {
	loginChatGPTCmd.Flags().Bool("new-account", false, "Register another ChatGPT account instead of signing in to the saved one")
	loginChatGPTCmd.Flags().Bool("no-browser", false, "Print the sign-in URL instead of opening a browser")
	loginChatGPTCmd.Flags().Int("port", chatgpt.PreferredPort, "Loopback callback port to try first (another free port is used if it is busy)")
	chatgptAskCmd.Flags().String("model", "", "Model slug (default: the first model from rsch chatgpt models)")
	loginCmd.AddCommand(loginChatGPTCmd)
	logoutCmd.AddCommand(logoutChatGPTCmd)
	chatgptCmd.AddCommand(chatgptStatusCmd, chatgptModelsCmd, chatgptAskCmd)
	rootCmd.AddCommand(loginCmd, logoutCmd, chatgptCmd)
}
