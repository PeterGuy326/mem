package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to memd (interactive email + password, dev only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig(server)
			if err != nil {
				return err
			}
			reader := bufio.NewReader(os.Stdin)
			fmt.Printf("Email (server=%s): ", cfg.Server)
			email, _ := reader.ReadString('\n')
			email = strings.TrimSpace(email)
			fmt.Print("Password: ")
			pw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}

			c := newHTTPClient(cfg)
			var resp struct {
				Token string `json:"token"`
				User  struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"user"`
			}
			err = c.doJSON("POST", "/v1/auth/login", map[string]string{
				"email":    email,
				"password": string(pw),
			}, &resp)
			if err != nil {
				return err
			}
			cfg.Email = resp.User.Email
			cfg.Token = resp.Token
			if err := saveConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in as %s; token saved to %s\n", resp.User.Email, "~/.mem/config.yaml")
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "memd base URL")
	return cmd
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear local CLI session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			cfg.Token = ""
			cfg.Email = ""
			if err := saveConfig(cfg); err != nil {
				return err
			}
			fmt.Println("logged out")
			return nil
		},
	}
}

func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens",
	}
	cmd.AddCommand(newTokenCreateCmd())
	cmd.AddCommand(newTokenListCmd())
	cmd.AddCommand(newTokenRevokeCmd())
	return cmd
}

func newTokenCreateCmd() *cobra.Command {
	var (
		name      string
		scopes    string
		expiresIn string
		format    string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token (plaintext shown once)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem login` first")
			}
			scopeList := splitCommas(scopes)
			body := map[string]any{
				"name":   name,
				"scopes": scopeList,
			}
			if expiresIn != "" {
				body["expires_in"] = expiresIn
			}
			var resp map[string]any
			c := newHTTPClient(cfg)
			if err := c.doJSON("POST", "/v1/auth/tokens", body, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp)
			}
			fmt.Printf("id:     %v\n", resp["id"])
			fmt.Printf("name:   %v\n", resp["name"])
			fmt.Printf("scopes: %v\n", resp["scopes"])
			if exp, ok := resp["expires_at"]; ok && exp != nil {
				fmt.Printf("expires_at: %v\n", exp)
			}
			fmt.Printf("\ntoken (shown once, store it now):\n  %v\n", resp["token"])
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "token name (required)")
	cmd.Flags().StringVar(&scopes, "scope", "read", "comma-separated scopes (search,read,write,delete,admin)")
	cmd.Flags().StringVar(&expiresIn, "expires", "", "Go duration (e.g. 720h)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newTokenListCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API tokens (without secrets)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			var resp struct {
				Tokens []map[string]any `json:"tokens"`
			}
			if err := c.doJSON("GET", "/v1/auth/tokens", nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp.Tokens)
			}
			fmt.Printf("%-36s  %-20s  %s\n", "ID", "NAME", "SCOPES")
			for _, t := range resp.Tokens {
				fmt.Printf("%-36v  %-20v  %v\n", t["ID"], t["Name"], t["Scopes"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func newTokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <token_id>",
		Short: "Revoke an API token by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			if err := c.doJSON("DELETE", "/v1/auth/tokens/"+args[0], nil, nil); err != nil {
				return err
			}
			fmt.Println("revoked")
			return nil
		},
	}
}

func splitCommas(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func jsonOut(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
