package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/skitchen1992/Ledger/ledger/app"
	"github.com/skitchen1992/Ledger/ledger/service"
)

func TestFactoriesRejectInvalidDatabaseConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://%not-a-valid-url")
	for _, factory := range factories() {
		t.Run(factory.name, func(t *testing.T) {
			ledgerService, closeFn, err := factory.new(context.Background())
			if ledgerService != nil || closeFn != nil || err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
				t.Fatalf("factory returned service=%v, closeFn present=%t, err=%v; want only a PostgreSQL error", ledgerService, closeFn != nil, err)
			}
		})
	}
}

func TestFactoriesPreserveStartupCancellation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, factory := range factories() {
		t.Run(factory.name, func(t *testing.T) {
			ledgerService, closeFn, err := factory.new(ctx)
			if ledgerService != nil || closeFn != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("factory returned service=%v, closeFn present=%t, err=%v; want only context.Canceled", ledgerService, closeFn != nil, err)
			}
		})
	}
}

func factories() []struct {
	name string
	new  func(context.Context) (service.Service, func(), error)
} {
	return []struct {
		name string
		new  func(context.Context) (service.Service, func(), error)
	}{
		{name: "Gateway", new: func(ctx context.Context) (service.Service, func(), error) {
			return app.New(ctx)
		}},
		{name: "reporting", new: func(ctx context.Context) (service.Service, func(), error) {
			return app.NewReporting(ctx)
		}},
	}
}
