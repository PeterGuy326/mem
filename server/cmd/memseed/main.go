package main

import (
	"context"
	"fmt"
	"os"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://mem:mem@localhost:5432/mem?sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	a := auth.New(pool)
	u, err := a.CreateUser(ctx, "demo@mem.dev", "demopass")
	if err != nil {
		fmt.Fprintln(os.Stderr, "user:", err)
	}
	if u == nil {
		u, err = a.VerifyPassword(ctx, "demo@mem.dev", "demopass")
		if err != nil {
			panic(err)
		}
	}
	fmt.Println("user_id:", u.ID)
	pt, t, err := a.CreateToken(ctx, u.ID, nil, "demo", []string{"admin", "write", "read", "search", "delete"}, []string{}, nil, false)
	if err != nil {
		panic(err)
	}
	fmt.Println("token_id:", t.ID)
	fmt.Println("token:", pt)
}
