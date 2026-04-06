package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/state"
)

// slackManifest is the Slack app manifest pre-filled with the scopes
// rat needs. The user creates the app from this — no manual scope config.
var slackManifest = map[string]interface{}{
	"display_information": map[string]interface{}{
		"name":        "rat",
		"description": "Run AnyThing — Slack kernel for rat",
	},
	"features": map[string]interface{}{
		"bot_user": map[string]interface{}{
			"display_name":  "rat",
			"always_online": false,
		},
	},
	"oauth_config": map[string]interface{}{
		"scopes": map[string]interface{}{
			"bot": []string{
				"channels:history",
				"channels:read",
				"chat:write",
				"groups:history",
				"groups:read",
				"im:history",
				"im:read",
				"im:write",
				"mpim:history",
				"mpim:read",
				"reactions:read",
				"reactions:write",
				"users:read",
			},
		},
	},
	"settings": map[string]interface{}{
		"event_subscriptions": map[string]interface{}{
			"bot_events": []string{
				"message.channels",
				"message.groups",
				"message.im",
				"message.mpim",
			},
		},
		"interactivity": map[string]interface{}{
			"is_enabled": false,
		},
		"org_deploy_enabled": false,
		"socket_mode_enabled": true,
		"token_rotation_enabled": false,
	},
}

func installSlackRuntime() error {
	fmt.Println("rat install slack")
	fmt.Println("")

	// Check if token already exists (env or stored in state).
	token := os.Getenv("SLACK_BOT_TOKEN")
	appToken := os.Getenv("SLACK_APP_TOKEN")

	s := store()

	// Check if there's a stored token in an existing slack runtime config.
	if token == "" || appToken == "" {
		runtimes, _ := s.ListRuntimes()
		for _, rt := range runtimes {
			if rt.Lang == "slack" && rt.Env != nil {
				if token == "" {
					if v, ok := rt.Env["SLACK_BOT_TOKEN"]; ok {
						token = v
					}
				}
				if appToken == "" {
					if v, ok := rt.Env["SLACK_APP_TOKEN"]; ok {
						appToken = v
					}
				}
			}
		}
	}

	if token != "" && appToken != "" {
		fmt.Println("Tokens already configured.")
		return finishSlackInstall(s, token, appToken)
	}

	if token != "" && appToken == "" {
		fmt.Println("Bot token found but app-level token missing.")
		fmt.Println("The app-level token (xapp-...) is needed for Socket Mode (real-time messages).")
		fmt.Println("")
		appToken = promptSlackAppToken()
		if appToken == "" {
			return fmt.Errorf("slack install cancelled")
		}
		return finishSlackInstall(s, token, appToken)
	}

	// No token — guide user through app creation.
	fmt.Println("Setting up Slack integration.")
	fmt.Println("")
	fmt.Println("This will:")
	fmt.Println("  1. Open Slack in your browser to create a pre-configured app")
	fmt.Println("  2. You install it to your workspace")
	fmt.Println("  3. Paste two tokens back here")
	fmt.Println("")

	// Build manifest URL.
	manifestJSON, _ := json.Marshal(slackManifest)
	manifestURL := "https://api.slack.com/apps?new_app=1&manifest_json=" + url.QueryEscape(string(manifestJSON))

	fmt.Println("Opening browser...")
	if err := openBrowser(manifestURL); err != nil {
		fmt.Println("Could not open browser. Open this URL manually:")
		fmt.Println("")
		fmt.Println("  " + manifestURL)
	}

	fmt.Println("")
	fmt.Println("In your browser:")
	fmt.Println("  1. Review the app config → Create")
	fmt.Println("  2. Go to \"OAuth & Permissions\" in the sidebar")
	fmt.Println("  3. Click \"Install to Workspace\" → Allow")
	fmt.Println("  4. Copy the \"Bot User OAuth Token\" (starts with xoxb-)")
	fmt.Println("")

	token = promptSlackBotToken()
	if token == "" {
		return fmt.Errorf("slack install cancelled")
	}

	fmt.Println("")
	fmt.Println("Now enable Socket Mode (for real-time messages):")
	fmt.Println("  5. Go to \"Basic Information\" in the sidebar")
	fmt.Println("  6. Scroll to \"App-Level Tokens\" → Generate Token")
	fmt.Println("     Name: \"rat\", Scope: connections:write → Generate")
	fmt.Println("  7. Copy the token (starts with xapp-)")
	fmt.Println("")

	appToken = promptSlackAppToken()
	if appToken == "" {
		return fmt.Errorf("slack install cancelled")
	}

	return finishSlackInstall(s, token, appToken)
}

func finishSlackInstall(s *state.Store, botToken, appToken string) error {
	// Resolve the runtime name for this project.
	r, err := resolveInput("slack")
	if err != nil {
		return err
	}

	// Merge tokens into the env map.
	env := r.Env
	if env == nil {
		env = make(map[string]string)
	}
	env["SLACK_BOT_TOKEN"] = botToken
	env["SLACK_APP_TOKEN"] = appToken

	// Stop any existing slack kernel.
	if existing, _ := s.GetRunning(r.Name); existing != nil {
		_ = daemon.Stop(s, r.Name)
		time.Sleep(500 * time.Millisecond)
	}

	// Update state entry with tokens.
	_ = s.PutRuntime(state.Runtime{
		Name: r.Name,
		Lang: r.Lang,
		Cwd:  r.Cwd,
		Env:  env,
	})

	// Start the kernel.
	k, err := daemon.Start(s, daemon.StartOpts{
		Name: r.Name,
		Lang: r.Lang,
		Cwd:  r.Cwd,
		Env:  env,
	})
	if err != nil {
		return err
	}

	// Smoke test.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := connectToKernel(ctx, r.Name)
	if err != nil {
		fmt.Println("")
		statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
		statusLine("smoke", false, "could not connect: "+err.Error())
		return err
	}
	defer session.Close()

	// Run /help to verify kernel works.
	result, err := session.Run(ctx, "/help")
	if err != nil {
		// Might be a token error.
		fmt.Println("")
		statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
		statusLine("smoke", false, err.Error())
		return fmt.Errorf("slack kernel started but smoke test failed: %w", err)
	}
	text := extractText(result)

	// Check for token errors.
	if strings.Contains(text, "SLACK_BOT_TOKEN not set") || strings.Contains(text, "auth failed") {
		fmt.Println("")
		statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
		statusLine("auth", false, text)
		return fmt.Errorf("slack authentication failed — check your token")
	}

	fmt.Println("")
	statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
	statusLine("auth", true, "connected to Slack")

	// Try to get channel info.
	statusResult, err := session.Ctl(ctx, "status")
	if err == nil {
		statusText := extractText(statusResult)
		if strings.Contains(statusText, "#") {
			statusLine("channel", true, statusText)
		}
	}

	fmt.Println("")
	fmt.Println("Ready.")
	fmt.Println("Try:")
	fmt.Println("  rat slack")
	fmt.Println("  rat run slack '/help'")
	fmt.Println("  rat run slack 'hello from rat'")
	fmt.Println("  rat run slack '/channels'")
	return nil
}

func promptSlackBotToken() string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Bot token (xoxb-...): ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(line)
	if !strings.HasPrefix(token, "xoxb-") {
		fmt.Println("Token should start with xoxb-")
		return ""
	}
	return token
}

func promptSlackAppToken() string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("App-level token (xapp-...): ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(line)
	if !strings.HasPrefix(token, "xapp-") {
		fmt.Println("Token should start with xapp-")
		return ""
	}
	return token
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
