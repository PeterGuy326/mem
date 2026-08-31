package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication and API tokens",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newLoginCmd())
	cmd.AddCommand(newLogoutCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newTokenCmd())
	return cmd
}

func newLegacyAuthCommand(cmd *cobra.Command, replacement string) *cobra.Command {
	cmd.Hidden = true
	cmd.Deprecated = fmt.Sprintf("use `%s` instead", replacement)
	return cmd
}

func newLegacyTokenCmd() *cobra.Command {
	cmd := newLegacyAuthCommand(newTokenCmd(), "mem auth token")
	for _, child := range cmd.Commands() {
		child.Deprecated = fmt.Sprintf(
			"use `mem auth token %s` instead",
			child.Name(),
		)
	}
	return cmd
}

func newLoginCmd() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to memd (interactive email + password, dev only)",
		Args:  cobra.NoArgs,
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
		Args:  cobra.NoArgs,
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

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Verify the current login and show its workspace access",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := cmd.Flags().GetString("format")
			if err != nil {
				return err
			}
			if format != "text" && format != "json" {
				return fmt.Errorf("--format must be text or json, got %q", format)
			}

			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}

			var capabilities struct {
				DeploymentMode   string          `json:"deployment_mode"`
				RegistrationMode string          `json:"registration_mode"`
				Workspace        authWorkspace   `json:"workspace"`
				Permissions      map[string]bool `json:"permissions"`
			}
			if err := newHTTPClient(cfg).doJSON(
				"GET",
				"/v1/capabilities",
				nil,
				&capabilities,
			); err != nil {
				return err
			}

			status := authStatus{
				LoggedIn:         true,
				Server:           cfg.Server,
				Email:            cfg.Email,
				Workspace:        capabilities.Workspace,
				Permissions:      capabilities.Permissions,
				DeploymentMode:   capabilities.DeploymentMode,
				RegistrationMode: capabilities.RegistrationMode,
			}
			if format == "json" {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(status)
			}
			printAuthStatus(cmd, status)
			return nil
		},
	}
}

type authWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type authStatus struct {
	LoggedIn         bool            `json:"logged_in"`
	Server           string          `json:"server"`
	Email            string          `json:"email,omitempty"`
	Workspace        authWorkspace   `json:"workspace"`
	Permissions      map[string]bool `json:"permissions"`
	DeploymentMode   string          `json:"deployment_mode,omitempty"`
	RegistrationMode string          `json:"registration_mode,omitempty"`
}

func printAuthStatus(cmd *cobra.Command, status authStatus) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "logged in")
	fmt.Fprintf(out, "server: %s\n", status.Server)
	if status.Email != "" {
		fmt.Fprintf(out, "email: %s\n", status.Email)
	}
	fmt.Fprintf(
		out,
		"workspace: %s (%s)\n",
		status.Workspace.Name,
		status.Workspace.ID,
	)
	fmt.Fprintf(out, "role: %s\n", status.Workspace.Role)

	permissions := make([]string, 0, len(status.Permissions))
	for permission, allowed := range status.Permissions {
		if allowed {
			permissions = append(permissions, permission)
		}
	}
	slices.Sort(permissions)
	fmt.Fprintf(out, "permissions: %s\n", strings.Join(permissions, ","))
}

func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens",
		Args:  cobra.NoArgs,
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
		paths     []string
		expiresIn string
		format    string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token (plaintext shown once)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}
			scopeList := splitCommas(scopes)
			body := map[string]any{
				"name":   name,
				"scopes": scopeList,
			}
			if len(paths) > 0 {
				body["paths"] = paths
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
			if p, ok := resp["paths"]; ok {
				fmt.Printf("paths:  %v\n", p)
			}
			if ws, ok := resp["workspace_id"]; ok && ws != nil {
				fmt.Printf("workspace: %v\n", ws)
			}
			if exp, ok := resp["expires_at"]; ok && exp != nil {
				fmt.Printf("expires_at: %v\n", exp)
			}
			fmt.Printf("\ntoken (shown once, store it now):\n  %v\n", resp["token"])
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "token name (required)")
	cmd.Flags().StringVar(&scopes, "scope", "read", "comma-separated scopes (search,read,write,delete,admin)")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "allowed virtual path; repeat for multiple roots")
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
		Args:  cobra.NoArgs,
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
